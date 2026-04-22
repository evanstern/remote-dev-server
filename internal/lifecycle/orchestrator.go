// Package lifecycle implements v2 orchestrator and feature state
// transitions backed by the SQLite store. This package is state-only:
// it records transitions and fires hooks. Actual tmux and opencode
// process lifecycle is managed by the bash wrapper outside this package.
package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/evanstern/coda/internal/hooks"
)

const (
	StateStopped   = "stopped"
	StateStarting  = "starting"
	StateRunning   = "running"
	StateStopping  = "stopping"
	StateStale     = "stale"
	StateSpawning  = "spawning"
	StateReporting = "reporting"
	StateDone      = "done"
	StateFailed    = "failed"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

// Orchestrator is one row in the orchestrators table.
type Orchestrator struct {
	ID          int64
	Name        string
	ConfigDir   string
	State       string
	TmuxSession sql.NullString
	Port        sql.NullInt64
	PID         sql.NullInt64
	StartedAt   sql.NullInt64
	StaleReason sql.NullString
	CreatedAt   int64
	UpdatedAt   int64
}

// Feature is one row in the features table.
type Feature struct {
	ID             int64
	Name           string
	OrchestratorID int64
	Project        string
	Branch         string
	WorktreeDir    string
	TmuxSession    sql.NullString
	State          string
	BriefPath      sql.NullString
	PRURL          sql.NullString
	StaleReason    sql.NullString
	CreatedAt      int64
	UpdatedAt      int64
	EndedAt        sql.NullInt64
}

// HookFirer abstracts hook dispatch so tests can stub it.
type HookFirer interface {
	Fire(ctx context.Context, event hooks.Event, payload map[string]any) error
}

// Manager is the stateful operations surface used by the CLI commands.
type Manager struct {
	DB     *sql.DB
	Hooks  HookFirer
	Now    func() time.Time
	prober Prober
}

// New constructs a Manager. A nil hooks dispatcher disables hook firing.
func New(db *sql.DB, h HookFirer) *Manager {
	return &Manager{DB: db, Hooks: h, Now: time.Now}
}

func (m *Manager) now() int64 { return m.Now().Unix() }

// CreateOrchestrator inserts a new orchestrator row in state=stopped.
func (m *Manager) CreateOrchestrator(ctx context.Context, name, configDir string) (*Orchestrator, error) {
	now := m.now()
	res, err := m.DB.ExecContext(ctx,
		`INSERT INTO orchestrators (name, config_dir, state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		name, configDir, StateStopped, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: orchestrator %q", ErrExists, name)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Orchestrator{
		ID: id, Name: name, ConfigDir: configDir,
		State: StateStopped, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// StartOrchestrator records a start transition: state=running, stamps
// tmux_session/port/pid/started_at. Fires post-orchestrator-start.
func (m *Manager) StartOrchestrator(ctx context.Context, name string, tmuxSession string, port int, pid int) (*Orchestrator, error) {
	now := m.now()
	res, err := m.DB.ExecContext(ctx,
		`UPDATE orchestrators
		 SET state=?, tmux_session=?, port=?, pid=?, started_at=?, stale_reason=NULL, updated_at=?
		 WHERE name=? AND state IN ('stopped','stale')`,
		StateRunning, nullStr(tmuxSession), nullInt(int64(port)), nullInt(int64(pid)), now, now, name)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: orchestrator %q (not in stopped|stale)", ErrNotFound, name)
	}
	o, err := m.GetOrchestrator(ctx, name)
	if err != nil {
		return nil, err
	}
	if m.Hooks != nil {
		if err := m.Hooks.Fire(ctx, hooks.EventPostOrchestratorStart, orchPayload(o)); err != nil {
			return nil, err
		}
	}
	return o, nil
}

// StopOrchestrator records a stop transition: state=stopped, clears
// port/pid. Fires post-orchestrator-stop.
func (m *Manager) StopOrchestrator(ctx context.Context, name string) (*Orchestrator, error) {
	now := m.now()
	res, err := m.DB.ExecContext(ctx,
		`UPDATE orchestrators
		 SET state=?, port=NULL, pid=NULL, updated_at=?
		 WHERE name=?`,
		StateStopped, now, name)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: orchestrator %q", ErrNotFound, name)
	}
	o, err := m.GetOrchestrator(ctx, name)
	if err != nil {
		return nil, err
	}
	if m.Hooks != nil {
		if err := m.Hooks.Fire(ctx, hooks.EventPostOrchestratorStop, orchPayload(o)); err != nil {
			return nil, err
		}
	}
	return o, nil
}

// GetOrchestrator fetches one row by name.
func (m *Manager) GetOrchestrator(ctx context.Context, name string) (*Orchestrator, error) {
	row := m.DB.QueryRowContext(ctx,
		`SELECT id, name, config_dir, state, tmux_session, port, pid, started_at, stale_reason, created_at, updated_at
		 FROM orchestrators WHERE name=?`, name)
	o := &Orchestrator{}
	err := row.Scan(&o.ID, &o.Name, &o.ConfigDir, &o.State, &o.TmuxSession,
		&o.Port, &o.PID, &o.StartedAt, &o.StaleReason, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: orchestrator %q", ErrNotFound, name)
	}
	return o, err
}

// ListOrchestrators returns all rows ordered by name.
func (m *Manager) ListOrchestrators(ctx context.Context) ([]*Orchestrator, error) {
	rows, err := m.DB.QueryContext(ctx,
		`SELECT id, name, config_dir, state, tmux_session, port, pid, started_at, stale_reason, created_at, updated_at
		 FROM orchestrators ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Orchestrator
	for rows.Next() {
		o := &Orchestrator{}
		if err := rows.Scan(&o.ID, &o.Name, &o.ConfigDir, &o.State, &o.TmuxSession,
			&o.Port, &o.PID, &o.StartedAt, &o.StaleReason, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// RemoveOrchestrator deletes one row. Features are removed via CASCADE.
func (m *Manager) RemoveOrchestrator(ctx context.Context, name string) error {
	res, err := m.DB.ExecContext(ctx, `DELETE FROM orchestrators WHERE name=?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: orchestrator %q", ErrNotFound, name)
	}
	return nil
}

func orchPayload(o *Orchestrator) map[string]any {
	p := map[string]any{
		"name":       o.Name,
		"config_dir": o.ConfigDir,
		"state":      o.State,
	}
	if o.TmuxSession.Valid {
		p["tmux_session"] = o.TmuxSession.String
	}
	if o.Port.Valid {
		p["port"] = o.Port.Int64
	}
	if o.PID.Valid {
		p["pid"] = o.PID.Int64
	}
	if o.StaleReason.Valid {
		p["stale_reason"] = o.StaleReason.String
	}
	return map[string]any{"orchestrator": p}
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsAny(msg, "UNIQUE constraint failed", "constraint failed: UNIQUE")
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		for i := 0; i+len(n) <= len(s); i++ {
			if s[i:i+len(n)] == n {
				return true
			}
		}
	}
	return false
}
