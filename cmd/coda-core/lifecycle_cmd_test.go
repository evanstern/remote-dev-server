package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/evanstern/coda/internal/db"
	"github.com/evanstern/coda/internal/hooks"
	"github.com/evanstern/coda/internal/lifecycle"
)

type countingProber struct {
	orchCalls int
	featCalls int
}

func (p *countingProber) OrchestratorAlive(o *lifecycle.Orchestrator) (bool, string, error) {
	p.orchCalls++
	return true, "", nil
}

func (p *countingProber) FeatureAlive(f *lifecycle.Feature) (bool, string, error) {
	p.featCalls++
	return true, "", nil
}

func newRateLimitManager(t *testing.T) *lifecycle.Manager {
	t.Helper()
	home := t.TempDir()
	d, err := db.Open(filepath.Join(home, "coda.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return lifecycle.New(d, hooks.New(d, home))
}

func seedProbeCandidate(t *testing.T, mgr *lifecycle.Manager) {
	t.Helper()
	ctx := context.Background()
	if _, err := mgr.CreateOrchestrator(ctx, "ash", "/tmp/ash"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.StartOrchestrator(ctx, "ash", "tmux-ash", "", 4096, 1111); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.DB.ExecContext(ctx,
		`UPDATE orchestrators SET updated_at=? WHERE name=?`, 1, "ash"); err != nil {
		t.Fatal(err)
	}
}

func TestMaybeLazyReconcile_SkipsWithinInterval(t *testing.T) {
	t.Setenv("CODA_NO_AUTO_RECONCILE", "")
	t.Setenv("CODA_RECONCILE_MIN_INTERVAL_SECS", "3600")

	mgr := newRateLimitManager(t)
	ctx := context.Background()
	seedProbeCandidate(t, mgr)

	probe := &countingProber{}
	mgr.SetProber(probe)

	maybeLazyReconcile(ctx, mgr)
	firstCalls := probe.orchCalls
	if firstCalls == 0 {
		t.Fatalf("first call expected to probe; orchCalls=0")
	}

	maybeLazyReconcile(ctx, mgr)
	maybeLazyReconcile(ctx, mgr)

	if probe.orchCalls != firstCalls {
		t.Fatalf("lazy reconcile ran more than once inside the rate-limit window: orchCalls=%d (first=%d)",
			probe.orchCalls, firstCalls)
	}
}

func TestMaybeLazyReconcile_RunsWhenIntervalZero(t *testing.T) {
	t.Setenv("CODA_NO_AUTO_RECONCILE", "")
	t.Setenv("CODA_RECONCILE_MIN_INTERVAL_SECS", "0")

	mgr := newRateLimitManager(t)
	ctx := context.Background()
	seedProbeCandidate(t, mgr)

	probe := &countingProber{}
	mgr.SetProber(probe)

	maybeLazyReconcile(ctx, mgr)
	maybeLazyReconcile(ctx, mgr)

	if probe.orchCalls < 2 {
		t.Fatalf("zero interval should not rate-limit; orchCalls=%d", probe.orchCalls)
	}
}

func TestMaybeLazyReconcile_OptOutSkipsEverything(t *testing.T) {
	t.Setenv("CODA_NO_AUTO_RECONCILE", "1")
	t.Setenv("CODA_RECONCILE_MIN_INTERVAL_SECS", "0")

	mgr := newRateLimitManager(t)
	ctx := context.Background()
	seedProbeCandidate(t, mgr)

	probe := &countingProber{}
	mgr.SetProber(probe)

	maybeLazyReconcile(ctx, mgr)
	if probe.orchCalls != 0 {
		t.Fatalf("CODA_NO_AUTO_RECONCILE=1 must disable lazy reconcile; orchCalls=%d", probe.orchCalls)
	}
}
