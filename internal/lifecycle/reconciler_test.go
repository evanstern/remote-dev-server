package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evanstern/coda/internal/db"
	"github.com/evanstern/coda/internal/hooks"
)

// stubProber gives tests per-(name) alive/dead answers for both
// orchestrators and features, modeling the same signal-combining policy
// the default prober enforces.
type stubProber struct {
	tmuxAlive map[string]bool
	pidAlive  map[int]bool
	tmuxErr   error
	pidErr    error
	orchErr   error
	featErr   error
}

func (s *stubProber) OrchestratorAlive(o *Orchestrator) (bool, string, error) {
	if s.orchErr != nil {
		return false, "", s.orchErr
	}
	hasTmux := o.TmuxSession.Valid && o.TmuxSession.String != ""
	hasPID := o.PID.Valid && o.PID.Int64 > 0

	if !hasTmux && !hasPID {
		return true, "no liveness signal", nil
	}

	var tmuxDead bool
	var tmuxReason string
	if hasTmux {
		if s.tmuxErr != nil {
			return false, "", s.tmuxErr
		}
		alive, known := s.tmuxAlive[o.TmuxSession.String]
		if known && alive {
			return true, "", nil
		}
		if known && !alive {
			tmuxDead = true
			tmuxReason = fmt.Sprintf("tmux session %q gone", o.TmuxSession.String)
		}
	}

	var pidDead bool
	var pidReason string
	if hasPID {
		if s.pidErr != nil {
			return false, "", s.pidErr
		}
		alive, known := s.pidAlive[int(o.PID.Int64)]
		if known && alive {
			return true, "", nil
		}
		if known && !alive {
			pidDead = true
			pidReason = fmt.Sprintf("pid %d not alive", o.PID.Int64)
		}
	}

	switch {
	case hasTmux && hasPID && tmuxDead && pidDead:
		return false, tmuxReason + " and " + pidReason, nil
	case hasTmux && !hasPID && tmuxDead:
		return false, tmuxReason, nil
	case hasPID && !hasTmux && pidDead:
		return false, pidReason, nil
	default:
		return true, "", nil
	}
}

func (s *stubProber) FeatureAlive(f *Feature) (bool, string, error) {
	if s.featErr != nil {
		return false, "", s.featErr
	}
	if !f.TmuxSession.Valid || f.TmuxSession.String == "" {
		return true, "no liveness signal", nil
	}
	if s.tmuxErr != nil {
		return false, "", s.tmuxErr
	}
	alive, known := s.tmuxAlive[f.TmuxSession.String]
	if !known {
		return true, "", nil
	}
	if alive {
		return true, "", nil
	}
	return false, fmt.Sprintf("tmux session %q gone", f.TmuxSession.String), nil
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

func TestReconcile_TmuxGoneWithUnknownPidKeepsAlive(t *testing.T) {
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
	if len(results) != 0 {
		t.Fatalf("tmux-dead but pid-alive should keep orch alive, got %+v", results)
	}
	o, _ := m.GetOrchestrator(ctx, "ash")
	if o.State != StateRunning {
		t.Fatalf("state=%s, want running", o.State)
	}
}

func TestReconcile_PidDeadButTmuxAliveKeepsAlive(t *testing.T) {
	m, _ := newReconcileManager(t)
	ctx := context.Background()

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartOrchestrator(ctx, "ash", "tmux-ash", 4096, 2222); err != nil {
		t.Fatal(err)
	}
	backdateOrchestrator(t, m, "ash", 60)

	m.SetProber(&stubProber{
		tmuxAlive: map[string]bool{"tmux-ash": true},
		pidAlive:  map[int]bool{2222: false},
	})

	results, err := m.Reconcile(ctx, "")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("pid-dead but tmux-alive should keep orch alive, got %+v", results)
	}
}

func TestReconcile_BothDeadMarksStale(t *testing.T) {
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
		pidAlive:  map[int]bool{1111: false},
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
	if !o.StaleReason.Valid ||
		!strings.Contains(o.StaleReason.String, "tmux session") ||
		!strings.Contains(o.StaleReason.String, "pid 1111") {
		t.Fatalf("stale_reason=%+v, want combined tmux+pid reason", o.StaleReason)
	}
}

func TestReconcile_TmuxOnlyDead(t *testing.T) {
	m, _ := newReconcileManager(t)
	ctx := context.Background()

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartOrchestrator(ctx, "ash", "tmux-ash", 4096, 0); err != nil {
		t.Fatal(err)
	}
	backdateOrchestrator(t, m, "ash", 60)

	m.SetProber(&stubProber{
		tmuxAlive: map[string]bool{"tmux-ash": false},
	})

	results, err := m.Reconcile(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%+v, want 1", results)
	}
	if !strings.Contains(results[0].StaleReason, "tmux-ash") {
		t.Fatalf("reason=%q missing session name", results[0].StaleReason)
	}
}

func TestReconcile_PidOnlyDead(t *testing.T) {
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
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%+v, want 1", results)
	}
	if !strings.Contains(results[0].StaleReason, "pid 2222") {
		t.Fatalf("reason=%q missing pid", results[0].StaleReason)
	}
}

func TestReconcile_NoSignalsSkipped(t *testing.T) {
	m, _ := newReconcileManager(t)
	ctx := context.Background()

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartOrchestrator(ctx, "ash", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	backdateOrchestrator(t, m, "ash", 60)

	m.SetProber(&stubProber{})
	results, err := m.Reconcile(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("row with no liveness signal must not be marked stale: %+v", results)
	}
	o, _ := m.GetOrchestrator(ctx, "ash")
	if o.State != StateRunning {
		t.Fatalf("state=%s, want running", o.State)
	}
}

func TestReconcile_ProbeErrorSkipsRow(t *testing.T) {
	m, _ := newReconcileManager(t)
	ctx := context.Background()

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartOrchestrator(ctx, "ash", "tmux-ash", 4096, 3333); err != nil {
		t.Fatal(err)
	}
	backdateOrchestrator(t, m, "ash", 60)

	m.SetProber(&stubProber{orchErr: errors.New("tmux missing")})
	results, err := m.Reconcile(ctx, "")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("probe error must skip row, got %+v", results)
	}
	o, _ := m.GetOrchestrator(ctx, "ash")
	if o.State != StateRunning {
		t.Fatalf("state=%s, want running (probe error must not mark stale)", o.State)
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
		pidAlive:  map[int]bool{5555: false},
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

func TestReconcile_ConcurrentStartWinsOverReconcile(t *testing.T) {
	m, _ := newReconcileManager(t)
	ctx := context.Background()

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartOrchestrator(ctx, "ash", "tmux-ash", 4096, 9999); err != nil {
		t.Fatal(err)
	}
	backdateOrchestrator(t, m, "ash", 60)

	o, _ := m.GetOrchestrator(ctx, "ash")
	origUpdated := o.UpdatedAt

	now := m.Now().Unix()
	if now <= origUpdated {
		now = origUpdated + 1
	}
	if _, err := m.DB.ExecContext(ctx,
		`UPDATE orchestrators SET state=?, tmux_session=?, pid=?, updated_at=? WHERE name=?`,
		StateRunning, "tmux-ash-restarted", 8888, now, "ash"); err != nil {
		t.Fatal(err)
	}

	m.SetProber(&stubProber{
		tmuxAlive: map[string]bool{"tmux-ash": false, "tmux-ash-restarted": true},
		pidAlive:  map[int]bool{9999: false, 8888: true},
	})

	results, err := m.Reconcile(ctx, "")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("concurrent restart should win; got %+v", results)
	}
	o2, _ := m.GetOrchestrator(ctx, "ash")
	if o2.State != StateRunning {
		t.Fatalf("state=%s, want running (post-restart row must not be clobbered)", o2.State)
	}
}

func TestReconcile_FeatureMarkedStale(t *testing.T) {
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
	if len(results) != 1 || results[0].Kind != "feature" || results[0].NewState != StateStale {
		t.Fatalf("results=%+v, want one stale feature", results)
	}

	f2, _ := m.GetFeature(ctx, "ash", "feat-x")
	if f2.State != StateStale {
		t.Fatalf("state=%s, want stale", f2.State)
	}
	if !f2.StaleReason.Valid || !strings.Contains(f2.StaleReason.String, "tmux session") {
		t.Fatalf("stale_reason=%+v", f2.StaleReason)
	}
}

func TestReconcile_FiresOrchestratorStaleHook(t *testing.T) {
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
		pidAlive:  map[int]bool{6666: false},
	})
	if _, err := m.Reconcile(ctx, ""); err != nil {
		t.Fatal(err)
	}

	if !spy.sawEvent(hooks.EventPostOrchestratorStale) {
		t.Fatalf("post-orchestrator-stale not fired; saw %v", spy.events)
	}
}

func TestReconcile_FiresFeatureStaleHook(t *testing.T) {
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
	if _, err := m.Reconcile(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if !spy.sawEvent(hooks.EventPostFeatureStale) {
		t.Fatalf("post-feature-stale not fired; saw %v", spy.events)
	}
}

// failingHookDispatcher returns an error for the first EventPostOrchestratorStale
// call, and records every event seen.
type failingHookDispatcher struct {
	events   []hooks.Event
	failOnce atomic.Bool
}

func (f *failingHookDispatcher) Fire(ctx context.Context, event hooks.Event, payload map[string]any) error {
	f.events = append(f.events, event)
	if event == hooks.EventPostOrchestratorStale && f.failOnce.CompareAndSwap(false, true) {
		return errors.New("injected hook failure")
	}
	return nil
}

func TestReconcile_HookFailureDoesNotAbortLoop(t *testing.T) {
	home := t.TempDir()
	d, err := db.Open(filepath.Join(home, "coda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	disp := &failingHookDispatcher{}
	m := New(d, disp)

	ctx := context.Background()
	for _, n := range []string{"ash", "beth"} {
		if _, err := m.CreateOrchestrator(ctx, n, "/tmp/"+n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.StartOrchestrator(ctx, "ash", "tmux-ash", 4096, 1111); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartOrchestrator(ctx, "beth", "tmux-beth", 4097, 2222); err != nil {
		t.Fatal(err)
	}
	backdateOrchestrator(t, m, "ash", 60)
	backdateOrchestrator(t, m, "beth", 60)

	m.SetProber(&stubProber{
		tmuxAlive: map[string]bool{"tmux-ash": false, "tmux-beth": false},
		pidAlive:  map[int]bool{1111: false, 2222: false},
	})

	results, err := m.Reconcile(ctx, "")
	if err != nil {
		t.Fatalf("reconcile must not surface hook failures: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("hook failure on first orch must not abort loop; got %d results", len(results))
	}
	for _, n := range []string{"ash", "beth"} {
		o, _ := m.GetOrchestrator(ctx, n)
		if o.State != StateStale {
			t.Fatalf("%s state=%s, want stale", n, o.State)
		}
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
		pidAlive:  map[int]bool{7777: false},
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
