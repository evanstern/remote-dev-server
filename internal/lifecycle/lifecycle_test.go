package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/evanstern/coda/internal/db"
	"github.com/evanstern/coda/internal/hooks"
)

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	home := t.TempDir()
	d, err := db.Open(filepath.Join(home, "coda.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return New(d, hooks.New(d, home)), home
}

func TestOrchestratorCRUD(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	o, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if o.State != StateStopped {
		t.Fatalf("new orch state=%s, want stopped", o.State)
	}

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate create: err=%v, want ErrExists", err)
	}

	o2, err := m.StartOrchestrator(ctx, "ash", "tmux-ash", 4096, 12345)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if o2.State != StateRunning {
		t.Fatalf("start state=%s, want running", o2.State)
	}
	if !o2.Port.Valid || o2.Port.Int64 != 4096 {
		t.Fatalf("port not persisted: %+v", o2.Port)
	}

	o3, err := m.StopOrchestrator(ctx, "ash")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if o3.State != StateStopped || o3.Port.Valid {
		t.Fatalf("stop didn't clear state/port: %+v", o3)
	}

	list, err := m.ListOrchestrators(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: len=%d err=%v", len(list), err)
	}

	if err := m.RemoveOrchestrator(ctx, "ash"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if _, err := m.GetOrchestrator(ctx, "ash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after rm: err=%v, want ErrNotFound", err)
	}
}

func TestFeatureCRUDAndCascade(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	if _, err := m.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}

	f, err := m.SpawnFeature(ctx, SpawnFeatureInput{
		OrchestratorName: "ash",
		Project:          "coda",
		Branch:           "feat-x",
		WorktreeDir:      "/tmp/coda/feat-x",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if f.State != StateSpawning || f.Name != "feat-x" {
		t.Fatalf("unexpected: %+v", f)
	}

	if _, err := m.AttachFeature(ctx, "ash", "feat-x"); err != nil {
		t.Fatalf("attach: %v", err)
	}

	feats, err := m.ListFeatures(ctx, "ash")
	if err != nil || len(feats) != 1 || feats[0].State != StateRunning {
		t.Fatalf("list features after attach: %+v err=%v", feats, err)
	}

	done, err := m.FinishFeature(ctx, "ash", "feat-x")
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if done.State != StateDone || !done.EndedAt.Valid {
		t.Fatalf("finish didn't mark done: %+v", done)
	}

	if err := m.RemoveOrchestrator(ctx, "ash"); err != nil {
		t.Fatalf("rm orch: %v", err)
	}
	feats, err = m.ListFeatures(ctx, "")
	if err != nil {
		t.Fatalf("list after cascade: %v", err)
	}
	if len(feats) != 0 {
		t.Fatalf("cascade delete failed, features remain: %+v", feats)
	}
}

func TestSpawnFeatureMissingOrchestrator(t *testing.T) {
	m, _ := newTestManager(t)
	_, err := m.SpawnFeature(context.Background(), SpawnFeatureInput{
		OrchestratorName: "ghost",
		Project:          "coda",
		Branch:           "x",
		WorktreeDir:      "/tmp/x",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
}
