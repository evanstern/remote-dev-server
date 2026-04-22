package lifecycle

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evanstern/coda/internal/db"
	"github.com/evanstern/coda/internal/hooks"
)

type stubProber struct {
	tmuxAlive map[string]bool
	pidAlive  map[int]bool
	tmuxErr   error
	pidErr    error
}

func (s *stubProber) TmuxSessionExists(name string) (bool, error) {
	if s.tmuxErr != nil {
		return false, s.tmuxErr
	}
	if v, ok := s.tmuxAlive[name]; ok {
		return v, nil
	}
	return false, nil
}

func (s *stubProber) PidAlive(pid int) (bool, error) {
	if s.pidErr != nil {
		return false, s.pidErr
	}
	if v, ok := s.pidAlive[pid]; ok {
		return v, nil
	}
	return false, nil
}

func newReconcileManager(t *testing.T) (*Manager, *hooks.Dispatcher) {
	t.Helper()
	home := t.TempDir()
	d, err := db.Open(filepath.Join(home, "coda.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	disp := hooks.New(d, home)
	return New(d, disp), disp
}

func backdateOrchestrator(t *testing.T, m *Manager, name string, seconds int64) {
	t.Helper()
	newTs := m.Now().Add(-time.Duration(seconds) * time.Second).Unix()
	if _, err := m.DB.Exec(`UPDATE orchestrators SET updated_at=? WHERE name=?`, newTs, name); err != nil {
		t.Fatalf("backdate: %v", err)
	}
}

func backdateFeature(t *testing.T, m *Manager, id int64, seconds int64) {
	t.Helper()
	newTs := m.Now().Add(-time.Duration(seconds) * time.Second).Unix()
	if _, err := m.DB.Exec(`UPDATE features SET updated_at=? WHERE id=?`, newTs, id); err != nil {
		t.Fatalf("backdate: %v", err)
	}
}

func TestReconcile_TmuxGoneMarksStale(t *testing.T) {
	m, _ := newReconcileManager(t)
	ctx := context.Background()

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartOrchestrator(ctx, "ash", "tmux-ash", 4096, 1111); err != nil {
		t.Fatal(err)
	}
	backdateOrchestrator(t, m, "ash", 60)

	m.SetProber(&stubProber{
		tmuxAlive: map[string]bool{"tmux-ash": false},
		pidAlive:  map[int]bool{1111: true},
	})

	results, err := m.Reconcile(ctx, "")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(results) != 1 || results[0].NewState != StateStale {
		t.Fatalf("results=%+v, want one stale", results)
	}

	o, _ := m.GetOrchestrator(ctx, "ash")
	if o.State != StateStale {
		t.Fatalf("state=%s, want stale", o.State)
	}
	if !o.StaleReason.Valid || !strings.Contains(o.StaleReason.String, "tmux session") {
		t.Fatalf("stale_reason=%+v, want 'tmux session...'", o.StaleReason)
	}
	if !strings.Contains(o.StaleReason.String, "tmux-ash") {
		t.Fatalf("stale_reason=%q missing session name", o.StaleReason.String)
	}
}

func TestReconcile_PidGoneMarksStale(t *testing.T) {
	m, _ := newReconcileManager(t)
	ctx := context.Background()

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartOrchestrator(ctx, "ash", "", 4096, 2222); err != nil {
		t.Fatal(err)
	}
	backdateOrchestrator(t, m, "ash", 60)

	m.SetProber(&stubProber{
		pidAlive: map[int]bool{2222: false},
	})

	results, err := m.Reconcile(ctx, "")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%+v, want 1", results)
	}

	o, _ := m.GetOrchestrator(ctx, "ash")
	if o.State != StateStale {
		t.Fatalf("state=%s, want stale", o.State)
	}
	if !strings.Contains(o.StaleReason.String, "pid 2222") {
		t.Fatalf("stale_reason=%q missing pid", o.StaleReason.String)
	}
}

func TestReconcile_BothAliveNoOp(t *testing.T) {
	m, _ := newReconcileManager(t)
	ctx := context.Background()

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartOrchestrator(ctx, "ash", "tmux-ash", 4096, 3333); err != nil {
		t.Fatal(err)
	}
	backdateOrchestrator(t, m, "ash", 60)

	m.SetProber(&stubProber{
		tmuxAlive: map[string]bool{"tmux-ash": true},
		pidAlive:  map[int]bool{3333: true},
	})

	results, err := m.Reconcile(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("results=%+v, want none", results)
	}
	o, _ := m.GetOrchestrator(ctx, "ash")
	if o.State != StateRunning {
		t.Fatalf("state=%s, want running", o.State)
	}
}

func TestReconcile_RespectsFreshnessWindow(t *testing.T) {
	m, _ := newReconcileManager(t)
	ctx := context.Background()

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartOrchestrator(ctx, "ash", "tmux-ash", 4096, 4444); err != nil {
		t.Fatal(err)
	}

	m.SetProber(&stubProber{
		tmuxAlive: map[string]bool{"tmux-ash": false},
		pidAlive:  map[int]bool{4444: false},
	})

	results, err := m.Reconcile(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("fresh row was reconciled: %+v", results)
	}
	o, _ := m.GetOrchestrator(ctx, "ash")
	if o.State != StateRunning {
		t.Fatalf("state=%s, want running (inside freshness window)", o.State)
	}
}

func TestReconcile_StaleRowIsIdempotent(t *testing.T) {
	m, _ := newReconcileManager(t)
	ctx := context.Background()

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartOrchestrator(ctx, "ash", "tmux-ash", 4096, 5555); err != nil {
		t.Fatal(err)
	}
	backdateOrchestrator(t, m, "ash", 60)

	m.SetProber(&stubProber{
		tmuxAlive: map[string]bool{"tmux-ash": false},
	})
	if _, err := m.Reconcile(ctx, ""); err != nil {
		t.Fatal(err)
	}
	backdateOrchestrator(t, m, "ash", 60)

	results, err := m.Reconcile(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("second reconcile produced results: %+v", results)
	}
}

func TestReconcile_FeatureMarkedFailed(t *testing.T) {
	m, _ := newReconcileManager(t)
	ctx := context.Background()

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	f, err := m.SpawnFeature(ctx, SpawnFeatureInput{
		OrchestratorName: "ash",
		Project:          "coda",
		Branch:           "feat-x",
		WorktreeDir:      "/tmp/x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DB.Exec(`UPDATE features SET tmux_session=? WHERE id=?`, "coda-feat-x", f.ID); err != nil {
		t.Fatal(err)
	}
	backdateFeature(t, m, f.ID, 60)

	m.SetProber(&stubProber{
		tmuxAlive: map[string]bool{"coda-feat-x": false},
	})

	results, err := m.Reconcile(ctx, "")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(results) != 1 || results[0].Kind != "feature" || results[0].NewState != StateFailed {
		t.Fatalf("results=%+v, want one failed feature", results)
	}

	f2, _ := m.GetFeature(ctx, "ash", "feat-x")
	if f2.State != StateFailed {
		t.Fatalf("state=%s, want failed", f2.State)
	}
	if !f2.StaleReason.Valid || !strings.Contains(f2.StaleReason.String, "tmux session") {
		t.Fatalf("stale_reason=%+v", f2.StaleReason)
	}
}

func TestReconcile_FiresHook(t *testing.T) {
	home := t.TempDir()
	d, err := db.Open(filepath.Join(home, "coda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	spy := &spyDispatcher{}
	m := New(d, spy)

	ctx := context.Background()
	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartOrchestrator(ctx, "ash", "tmux-ash", 4096, 6666); err != nil {
		t.Fatal(err)
	}
	backdateOrchestrator(t, m, "ash", 60)

	m.SetProber(&stubProber{
		tmuxAlive: map[string]bool{"tmux-ash": false},
	})
	if _, err := m.Reconcile(ctx, ""); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, e := range spy.events {
		if e == hooks.EventPostOrchestratorStale {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("post-orchestrator-stale not fired; saw %v", spy.events)
	}
}

func TestStartOrchestrator_FromStale(t *testing.T) {
	m, _ := newReconcileManager(t)
	ctx := context.Background()

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartOrchestrator(ctx, "ash", "tmux-ash", 4096, 7777); err != nil {
		t.Fatal(err)
	}
	backdateOrchestrator(t, m, "ash", 60)

	m.SetProber(&stubProber{
		tmuxAlive: map[string]bool{"tmux-ash": false},
	})
	if _, err := m.Reconcile(ctx, ""); err != nil {
		t.Fatal(err)
	}

	o, _ := m.GetOrchestrator(ctx, "ash")
	if o.State != StateStale {
		t.Fatalf("precondition: state=%s, want stale", o.State)
	}

	o2, err := m.StartOrchestrator(ctx, "ash", "tmux-ash-2", 4097, 8888)
	if err != nil {
		t.Fatalf("start from stale: %v", err)
	}
	if o2.State != StateRunning {
		t.Fatalf("state=%s, want running", o2.State)
	}
	if o2.StaleReason.Valid {
		t.Fatalf("stale_reason not cleared: %+v", o2.StaleReason)
	}
}
