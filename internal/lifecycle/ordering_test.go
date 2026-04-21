package lifecycle

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/evanstern/coda/internal/db"
	"github.com/evanstern/coda/internal/hooks"
)

type spyDispatcher struct {
	onFire func(event hooks.Event) error
	events []hooks.Event
}

func (s *spyDispatcher) Fire(ctx context.Context, e hooks.Event, _ map[string]any) error {
	s.events = append(s.events, e)
	if s.onFire != nil {
		return s.onFire(e)
	}
	return nil
}

func TestPreFeatureTeardownFiresBeforeStateUpdate(t *testing.T) {
	home := t.TempDir()
	d, err := db.Open(filepath.Join(home, "coda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var observedState string
	spy := &spyDispatcher{
		onFire: func(e hooks.Event) error {
			if e != hooks.EventPreFeatureTeardown {
				return nil
			}
			if err := d.QueryRow(
				"SELECT state FROM features WHERE name='feat-x'",
			).Scan(&observedState); err != nil {
				t.Fatalf("probe: %v", err)
			}
			return nil
		},
	}
	m := New(d, spy)

	ctx := context.Background()
	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SpawnFeature(ctx, SpawnFeatureInput{
		OrchestratorName: "ash",
		Project:          "coda",
		Branch:           "feat-x",
		WorktreeDir:      "/tmp/x",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AttachFeature(ctx, "ash", "feat-x"); err != nil {
		t.Fatal(err)
	}

	if _, err := m.FinishFeature(ctx, "ash", "feat-x"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if observedState != StateRunning {
		t.Fatalf("pre-feature-teardown observed state=%q; want %q (must fire BEFORE row update)",
			observedState, StateRunning)
	}

	f, err := m.GetFeature(ctx, "ash", "feat-x")
	if err != nil {
		t.Fatal(err)
	}
	if f.State != StateDone {
		t.Fatalf("after finish state=%s, want done", f.State)
	}

	if len(spy.events) != 2 {
		t.Fatalf("expected 2 hook events, got %v", spy.events)
	}
	if spy.events[0] != hooks.EventPostFeatureSpawn {
		t.Fatalf("expected first event = post-feature-spawn, got %v", spy.events[0])
	}
	if spy.events[1] != hooks.EventPreFeatureTeardown {
		t.Fatalf("expected second event = pre-feature-teardown, got %v", spy.events[1])
	}
}
