package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// Prober verifies whether a process or tmux session is alive. Production
// uses tmuxPidProber; tests inject stubs.
type Prober interface {
	TmuxSessionExists(name string) (bool, error)
	PidAlive(pid int) (bool, error)
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
// spawning|running) and marks rows whose backing tmux session or pid no
// longer exists as stale/failed. If name is empty all candidates are
// checked; otherwise only that orchestrator (and its features).
//
// Rows updated within reconcileFreshnessWindow are skipped to avoid
// racing in-flight Start transitions. Rows already terminal (stale,
// done, failed, stopped) are skipped.
//
// Reconcile is observational: it updates DB rows and fires the
// post-orchestrator-stale hook. It does not kill tmux sessions or pids.
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
		reason := orchDeadReason(p, o)
		if reason == "" {
			continue
		}
		prev := o.State
		if err := m.markOrchestratorStale(ctx, o, reason); err != nil {
			return results, err
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

func orchDeadReason(p Prober, o *Orchestrator) string {
	if o.TmuxSession.Valid && o.TmuxSession.String != "" {
		alive, err := p.TmuxSessionExists(o.TmuxSession.String)
		if err == nil && !alive {
			return fmt.Sprintf("tmux session %q gone", o.TmuxSession.String)
		}
	}
	if o.PID.Valid && o.PID.Int64 > 0 {
		alive, err := p.PidAlive(int(o.PID.Int64))
		if err == nil && !alive {
			return fmt.Sprintf("pid %d not alive", o.PID.Int64)
		}
	}
	return ""
}

func (m *Manager) markOrchestratorStale(ctx context.Context, o *Orchestrator, reason string) error {
	now := m.Now().Unix()
	_, err := m.DB.ExecContext(ctx,
		`UPDATE orchestrators SET state=?, stale_reason=?, updated_at=? WHERE id=?`,
		StateStale, reason, now, o.ID)
	if err != nil {
		return err
	}
	o.State = StateStale
	o.StaleReason = sql.NullString{String: reason, Valid: true}
	o.UpdatedAt = now
	if m.Hooks != nil {
		if err := m.Hooks.Fire(ctx, hooks.EventPostOrchestratorStale, orchPayload(o)); err != nil {
			return err
		}
	}
	return nil
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
		if !c.feat.TmuxSession.Valid || c.feat.TmuxSession.String == "" {
			continue
		}
		alive, err := p.TmuxSessionExists(c.feat.TmuxSession.String)
		if err != nil || alive {
			continue
		}
		reason := fmt.Sprintf("tmux session %q gone", c.feat.TmuxSession.String)
		prev := c.feat.State
		if err := m.markFeatureFailed(ctx, c.feat.ID, reason); err != nil {
			return results, err
		}
		results = append(results, ReconcileResult{
			Kind:        "feature",
			Name:        c.feat.Name,
			Orch:        c.orch,
			PreviousSt:  prev,
			NewState:    StateFailed,
			StaleReason: reason,
		})
	}
	return results, nil
}

func (m *Manager) markFeatureFailed(ctx context.Context, id int64, reason string) error {
	now := m.Now().Unix()
	_, err := m.DB.ExecContext(ctx,
		`UPDATE features SET state=?, stale_reason=?, ended_at=?, updated_at=? WHERE id=?`,
		StateFailed, reason, now, now, id)
	return err
}

type defaultProber struct{}

func (defaultProber) TmuxSessionExists(name string) (bool, error) {
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
	if errors.Is(err, exec.ErrNotFound) {
		return false, nil
	}
	return false, err
}

func (defaultProber) PidAlive(pid int) (bool, error) {
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
		return false, nil
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
