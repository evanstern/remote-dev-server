package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/evanstern/coda/internal/hooks"
)

// reconcileFreshnessWindow protects in-flight StartOrchestrator calls
// whose row has a pid/tmux_session recorded before the child has fully
// forked. Rows updated inside this window are skipped by Reconcile.
const reconcileFreshnessWindow = 30 * time.Second

// Prober answers "is this row's backing process/session still alive?"
// The default implementation encapsulates signal-combining policy so the
// reconciler loop never has to make that decision itself: both
// known-dead signals are required to declare an orchestrator dead, a
// single unknown signal doesn't move the row, and any probe error
// causes the row to be skipped rather than wrongly marked stale.
type Prober interface {
	OrchestratorAlive(o *Orchestrator) (alive bool, reason string, err error)
	FeatureAlive(f *Feature) (alive bool, reason string, err error)
}

// ReconcileResult describes a single row transitioned by Reconcile.
type ReconcileResult struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Orch        string `json:"orchestrator,omitempty"`
	PreviousSt  string `json:"previous_state"`
	NewState    string `json:"new_state"`
	StaleReason string `json:"stale_reason"`
}

// SetProber overrides the default tmux/pid prober. Tests use this to
// inject deterministic behavior. Passing nil resets to the default.
func (m *Manager) SetProber(p Prober) {
	m.prober = p
}

func (m *Manager) probe() Prober {
	if m.prober != nil {
		return m.prober
	}
	return defaultProber{}
}

// Reconcile inspects lifecycle rows whose state suggests they should be
// alive (orchestrators in starting|running|stopping; features in
// spawning|running|reporting) and marks rows whose backing tmux session
// or pid no longer exists as stale. If name is empty all candidates are
// checked; otherwise only that orchestrator (and its features).
//
// Rows updated within reconcileFreshnessWindow are skipped to avoid
// racing in-flight Start transitions. Rows already terminal (stale,
// done, failed, stopped) are skipped.
//
// Reconcile is observational: it updates DB rows and fires the
// post-orchestrator-stale / post-feature-stale hooks. It does not kill
// tmux sessions or pids. Hook failures are logged and do not abort the
// loop — reconciliation correctness does not depend on hook delivery.
func (m *Manager) Reconcile(ctx context.Context, name string) ([]ReconcileResult, error) {
	cutoff := m.Now().Add(-reconcileFreshnessWindow).Unix()
	var results []ReconcileResult

	orchs, err := m.reconcileOrchestrators(ctx, name, cutoff)
	if err != nil {
		return results, err
	}
	results = append(results, orchs...)

	feats, err := m.reconcileFeatures(ctx, name, cutoff)
	if err != nil {
		return results, err
	}
	results = append(results, feats...)

	return results, nil
}

func (m *Manager) reconcileOrchestrators(ctx context.Context, name string, cutoff int64) ([]ReconcileResult, error) {
	query := `SELECT id, name, config_dir, state, tmux_session, port, pid, started_at, stale_reason, created_at, updated_at
	          FROM orchestrators
	          WHERE state IN ('starting','running','stopping')
	            AND updated_at <= ?`
	args := []any{cutoff}
	if name != "" {
		query += ` AND name = ?`
		args = append(args, name)
	}

	rows, err := m.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []*Orchestrator
	for rows.Next() {
		o := &Orchestrator{}
		if err := rows.Scan(&o.ID, &o.Name, &o.ConfigDir, &o.State, &o.TmuxSession,
			&o.Port, &o.PID, &o.StartedAt, &o.StaleReason, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		candidates = append(candidates, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	p := m.probe()
	var results []ReconcileResult
	for _, o := range candidates {
		alive, reason, err := p.OrchestratorAlive(o)
		if err != nil {
			log.Printf("reconcile: skipping orchestrator %q: probe error: %v", o.Name, err)
			continue
		}
		if alive {
			continue
		}
		prev := o.State
		prevUpdatedAt := o.UpdatedAt
		transitioned, err := m.markOrchestratorStale(ctx, o, prev, prevUpdatedAt, reason)
		if err != nil {
			return results, err
		}
		if !transitioned {
			continue
		}
		results = append(results, ReconcileResult{
			Kind:        "orchestrator",
			Name:        o.Name,
			PreviousSt:  prev,
			NewState:    StateStale,
			StaleReason: reason,
		})
	}
	return results, nil
}

// markOrchestratorStale transitions an orchestrator row to stale with
// an optimistic UPDATE whose WHERE clause pins the row's state and
// updated_at to the values this reconcile pass read at SELECT time. A
// concurrent StartOrchestrator / StopOrchestrator between the SELECT
// and this UPDATE causes RowsAffected to be zero, in which case the
// function returns (false, nil) and the hook does not fire. Hook
// errors are logged; they do not propagate, so a misbehaving hook
// cannot abort the surrounding Reconcile loop.
func (m *Manager) markOrchestratorStale(ctx context.Context, o *Orchestrator, prevState string, prevUpdatedAt int64, reason string) (bool, error) {
	now := m.Now().Unix()
	res, err := m.DB.ExecContext(ctx,
		`UPDATE orchestrators SET state=?, stale_reason=?, updated_at=?
		 WHERE id=? AND state=? AND updated_at=?`,
		StateStale, reason, now, o.ID, prevState, prevUpdatedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	o.State = StateStale
	o.StaleReason.String = reason
	o.StaleReason.Valid = true
	o.UpdatedAt = now
	if m.Hooks != nil {
		if err := m.Hooks.Fire(ctx, hooks.EventPostOrchestratorStale, orchPayload(o)); err != nil {
			log.Printf("reconcile: post-orchestrator-stale hook for %q failed: %v", o.Name, err)
		}
	}
	return true, nil
}

func (m *Manager) reconcileFeatures(ctx context.Context, orchName string, cutoff int64) ([]ReconcileResult, error) {
	query := `SELECT f.id, f.name, f.orchestrator_id, f.project, f.branch, f.worktree_dir,
	                 f.tmux_session, f.state, f.brief_path, f.pr_url, f.stale_reason,
	                 f.created_at, f.updated_at, f.ended_at,
	                 o.name
	          FROM features f
	          JOIN orchestrators o ON o.id = f.orchestrator_id
	          WHERE f.state IN ('spawning','running','reporting')
	            AND f.updated_at <= ?`
	args := []any{cutoff}
	if orchName != "" {
		query += ` AND o.name = ?`
		args = append(args, orchName)
	}

	rows, err := m.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type candidate struct {
		feat *Feature
		orch string
	}
	var candidates []candidate
	for rows.Next() {
		f := &Feature{}
		var orch string
		if err := rows.Scan(&f.ID, &f.Name, &f.OrchestratorID, &f.Project, &f.Branch,
			&f.WorktreeDir, &f.TmuxSession, &f.State, &f.BriefPath, &f.PRURL, &f.StaleReason,
			&f.CreatedAt, &f.UpdatedAt, &f.EndedAt, &orch); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate{feat: f, orch: orch})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	p := m.probe()
	var results []ReconcileResult
	for _, c := range candidates {
		alive, reason, err := p.FeatureAlive(c.feat)
		if err != nil {
			log.Printf("reconcile: skipping feature %q on %q: probe error: %v", c.feat.Name, c.orch, err)
			continue
		}
		if alive {
			continue
		}
		prev := c.feat.State
		prevUpdatedAt := c.feat.UpdatedAt
		transitioned, err := m.markFeatureStale(ctx, c.feat, c.orch, prev, prevUpdatedAt, reason)
		if err != nil {
			return results, err
		}
		if !transitioned {
			continue
		}
		results = append(results, ReconcileResult{
			Kind:        "feature",
			Name:        c.feat.Name,
			Orch:        c.orch,
			PreviousSt:  prev,
			NewState:    StateStale,
			StaleReason: reason,
		})
	}
	return results, nil
}

// markFeatureStale writes state='stale' on a feature via an optimistic
// UPDATE pinned to the (state, updated_at) this reconcile pass read at
// SELECT time. The extra IN-list predicate guards against a concurrent
// writer moving the row out of the candidate vocabulary entirely
// (e.g. spawning -> done). Zero RowsAffected means another writer won
// the race; this function returns (false, nil) without firing the hook.
func (m *Manager) markFeatureStale(ctx context.Context, f *Feature, orchName, prevState string, prevUpdatedAt int64, reason string) (bool, error) {
	now := m.Now().Unix()
	res, err := m.DB.ExecContext(ctx,
		`UPDATE features
		 SET state=?, stale_reason=?, ended_at=?, updated_at=?
		 WHERE id=? AND state=? AND updated_at=?
		   AND state IN ('spawning','running','reporting')`,
		StateStale, reason, now, now, f.ID, prevState, prevUpdatedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	f.State = StateStale
	f.StaleReason.String = reason
	f.StaleReason.Valid = true
	f.UpdatedAt = now
	f.EndedAt.Int64 = now
	f.EndedAt.Valid = true
	if m.Hooks != nil {
		payload := featureStalePayload(f, orchName)
		if err := m.Hooks.Fire(ctx, hooks.EventPostFeatureStale, payload); err != nil {
			log.Printf("reconcile: post-feature-stale hook for %q failed: %v", f.Name, err)
		}
	}
	return true, nil
}

func featureStalePayload(f *Feature, orchName string) map[string]any {
	p := map[string]any{
		"name":         f.Name,
		"orchestrator": orchName,
		"project":      f.Project,
		"branch":       f.Branch,
		"worktree_dir": f.WorktreeDir,
		"state":        f.State,
	}
	if f.TmuxSession.Valid {
		p["tmux_session"] = f.TmuxSession.String
	}
	if f.BriefPath.Valid {
		p["brief_path"] = f.BriefPath.String
	}
	if f.StaleReason.Valid {
		p["stale_reason"] = f.StaleReason.String
	}
	return map[string]any{"feature": p}
}

type defaultProber struct{}

// OrchestratorAlive combines the tmux-session and pid signals under an
// explicit policy: a single live signal is enough to keep the row,
// unknown signals (empty session name, zero pid) are treated as
// no-information rather than dead, and the row is only declared dead
// when every known signal reports dead. Any probe error propagates so
// the caller skips the row instead of marking it wrongly stale.
func (defaultProber) OrchestratorAlive(o *Orchestrator) (bool, string, error) {
	hasTmux := o.TmuxSession.Valid && o.TmuxSession.String != ""
	hasPID := o.PID.Valid && o.PID.Int64 > 0

	if !hasTmux && !hasPID {
		return true, "no liveness signal", nil
	}

	var tmuxDead bool
	var tmuxReason string
	if hasTmux {
		alive, err := tmuxSessionExists(o.TmuxSession.String)
		if err != nil {
			return false, "", err
		}
		if alive {
			return true, "", nil
		}
		tmuxDead = true
		tmuxReason = fmt.Sprintf("tmux session %q gone", o.TmuxSession.String)
	}

	var pidDead bool
	var pidReason string
	if hasPID {
		alive, err := pidAlive(int(o.PID.Int64))
		if err != nil {
			return false, "", err
		}
		if alive {
			return true, "", nil
		}
		pidDead = true
		pidReason = fmt.Sprintf("pid %d not alive", o.PID.Int64)
	}

	switch {
	case tmuxDead && pidDead:
		return false, tmuxReason + " and " + pidReason, nil
	case tmuxDead:
		return false, tmuxReason, nil
	case pidDead:
		return false, pidReason, nil
	default:
		return true, "", nil
	}
}

// FeatureAlive probes tmux only: the features schema has no pid column.
// Features with no recorded tmux session have no liveness signal and
// are left untouched.
func (defaultProber) FeatureAlive(f *Feature) (bool, string, error) {
	if !f.TmuxSession.Valid || f.TmuxSession.String == "" {
		return true, "no liveness signal", nil
	}
	alive, err := tmuxSessionExists(f.TmuxSession.String)
	if err != nil {
		return false, "", err
	}
	if alive {
		return true, "", nil
	}
	return false, fmt.Sprintf("tmux session %q gone", f.TmuxSession.String), nil
}

// tmuxSessionExists reports whether `tmux has-session -t name` succeeds.
// A non-zero exit from tmux itself means "session absent" (alive=false,
// nil err). Any other failure — including tmux missing from PATH —
// surfaces as a non-nil error so the caller can skip the row instead
// of wrongly declaring every orchestrator stale.
func tmuxSessionExists(name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	cmd := exec.Command("tmux", "has-session", "-t", name)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return false, nil
	}
	return false, err
}

// pidAlive reports whether pid is a running, non-zombie process.
// /proc/<pid>/status is consulted first on Linux; the signal-0 fallback
// is used elsewhere. EPERM from kill(2) means the process exists but
// we're not allowed to signal it — the process IS alive.
func pidAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	if alive, ok := pidAliveProc(pid); ok {
		return alive, nil
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	return false, err
}

// pidAliveProc consults /proc on Linux. Returns (alive, handled); if
// handled=false the caller falls back to signal-0. A zombie is treated
// as not alive.
func pidAliveProc(pid int) (bool, bool) {
	status, err := os.ReadFile(filepath.Join("/proc", fmt.Sprintf("%d", pid), "status"))
	if err != nil {
		if os.IsNotExist(err) {
			if _, statErr := os.Stat("/proc/self"); statErr == nil {
				return false, true
			}
		}
		return false, false
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "State:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == "Z" {
				return false, true
			}
			return true, true
		}
	}
	return true, true
}
