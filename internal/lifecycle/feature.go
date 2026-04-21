package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/evanstern/coda/internal/hooks"
)

// SpawnFeatureInput carries the arguments for SpawnFeature.
type SpawnFeatureInput struct {
	OrchestratorName string
	Name             string
	Project          string
	Branch           string
	WorktreeDir      string
	BriefPath        string
}

// SpawnFeature inserts a new feature row (state=spawning) and fires
// post-feature-spawn.
func (m *Manager) SpawnFeature(ctx context.Context, in SpawnFeatureInput) (*Feature, error) {
	orch, err := m.GetOrchestrator(ctx, in.OrchestratorName)
	if err != nil {
		return nil, err
	}
	now := m.now()

	featureName := in.Name
	if featureName == "" {
		featureName = in.Branch
	}

	res, err := m.DB.ExecContext(ctx,
		`INSERT INTO features
		 (name, orchestrator_id, project, branch, worktree_dir, state, brief_path, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		featureName, orch.ID, in.Project, in.Branch, in.WorktreeDir,
		StateSpawning, nullStr(in.BriefPath), now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: feature %q on orchestrator %q", ErrExists, featureName, in.OrchestratorName)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	f := &Feature{
		ID: id, Name: featureName, OrchestratorID: orch.ID,
		Project: in.Project, Branch: in.Branch, WorktreeDir: in.WorktreeDir,
		State: StateSpawning, CreatedAt: now, UpdatedAt: now,
	}
	if in.BriefPath != "" {
		f.BriefPath = sql.NullString{String: in.BriefPath, Valid: true}
	}
	if m.Hooks != nil {
		if err := m.Hooks.Fire(ctx, hooks.EventPostFeatureSpawn, featurePayload(f, orch)); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// AttachFeature marks a feature as running.
func (m *Manager) AttachFeature(ctx context.Context, orchName, name string) (*Feature, error) {
	orch, err := m.GetOrchestrator(ctx, orchName)
	if err != nil {
		return nil, err
	}
	now := m.now()
	res, err := m.DB.ExecContext(ctx,
		`UPDATE features SET state=?, updated_at=? WHERE orchestrator_id=? AND name=?`,
		StateRunning, now, orch.ID, name)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: feature %q on orchestrator %q", ErrNotFound, name, orchName)
	}
	return m.GetFeature(ctx, orchName, name)
}

// FinishFeature fires pre-feature-teardown BEFORE marking the row done.
// The ordering guarantee lets hooks observe the still-running state.
func (m *Manager) FinishFeature(ctx context.Context, orchName, name string) (*Feature, error) {
	orch, err := m.GetOrchestrator(ctx, orchName)
	if err != nil {
		return nil, err
	}
	f, err := m.GetFeature(ctx, orchName, name)
	if err != nil {
		return nil, err
	}

	if m.Hooks != nil {
		if err := m.Hooks.Fire(ctx, hooks.EventPreFeatureTeardown, featurePayload(f, orch)); err != nil {
			return nil, err
		}
	}

	now := m.now()
	_, err = m.DB.ExecContext(ctx,
		`UPDATE features SET state=?, ended_at=?, updated_at=? WHERE id=?`,
		StateDone, now, now, f.ID)
	if err != nil {
		return nil, err
	}
	return m.GetFeature(ctx, orchName, name)
}

// GetFeature fetches a feature by (orchestrator, name).
func (m *Manager) GetFeature(ctx context.Context, orchName, name string) (*Feature, error) {
	row := m.DB.QueryRowContext(ctx,
		`SELECT f.id, f.name, f.orchestrator_id, f.project, f.branch, f.worktree_dir,
		        f.tmux_session, f.state, f.brief_path, f.pr_url,
		        f.created_at, f.updated_at, f.ended_at
		 FROM features f
		 JOIN orchestrators o ON o.id = f.orchestrator_id
		 WHERE o.name=? AND f.name=?`, orchName, name)
	f := &Feature{}
	err := row.Scan(&f.ID, &f.Name, &f.OrchestratorID, &f.Project, &f.Branch,
		&f.WorktreeDir, &f.TmuxSession, &f.State, &f.BriefPath, &f.PRURL,
		&f.CreatedAt, &f.UpdatedAt, &f.EndedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: feature %q on orchestrator %q", ErrNotFound, name, orchName)
	}
	return f, err
}

// ListFeatures returns features, optionally filtered by orchestrator.
func (m *Manager) ListFeatures(ctx context.Context, orchName string) ([]*Feature, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if orchName == "" {
		rows, err = m.DB.QueryContext(ctx,
			`SELECT id, name, orchestrator_id, project, branch, worktree_dir,
			        tmux_session, state, brief_path, pr_url,
			        created_at, updated_at, ended_at
			 FROM features ORDER BY orchestrator_id, name`)
	} else {
		rows, err = m.DB.QueryContext(ctx,
			`SELECT f.id, f.name, f.orchestrator_id, f.project, f.branch, f.worktree_dir,
			        f.tmux_session, f.state, f.brief_path, f.pr_url,
			        f.created_at, f.updated_at, f.ended_at
			 FROM features f JOIN orchestrators o ON o.id=f.orchestrator_id
			 WHERE o.name=? ORDER BY f.name`, orchName)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Feature
	for rows.Next() {
		f := &Feature{}
		if err := rows.Scan(&f.ID, &f.Name, &f.OrchestratorID, &f.Project, &f.Branch,
			&f.WorktreeDir, &f.TmuxSession, &f.State, &f.BriefPath, &f.PRURL,
			&f.CreatedAt, &f.UpdatedAt, &f.EndedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func featurePayload(f *Feature, orch *Orchestrator) map[string]any {
	p := map[string]any{
		"name":         f.Name,
		"orchestrator": orch.Name,
		"project":      f.Project,
		"branch":       f.Branch,
		"worktree_dir": f.WorktreeDir,
		"state":        f.State,
	}
	if f.BriefPath.Valid {
		p["brief_path"] = f.BriefPath.String
	}
	if f.TmuxSession.Valid {
		p["tmux_session"] = f.TmuxSession.String
	}
	return map[string]any{"feature": p}
}
