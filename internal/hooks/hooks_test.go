package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/evanstern/coda/internal/db"
)

func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestFire_NoDirIsNoOp(t *testing.T) {
	home := t.TempDir()
	d, err := db.Open(filepath.Join(home, "coda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	disp := New(d, home)
	if err := disp.Fire(context.Background(), EventPostOrchestratorStart, nil); err != nil {
		t.Fatalf("fire: %v", err)
	}
	var n int
	d.QueryRow("SELECT count(*) FROM hook_events").Scan(&n)
	if n != 0 {
		t.Fatalf("expected 0 events, got %d", n)
	}
}

func TestFire_RecordsSuccessAndFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash scripts")
	}
	home := t.TempDir()
	d, err := db.Open(filepath.Join(home, "coda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	eventDir := filepath.Join(home, "hooks", string(EventPostFeatureSpawn))
	writeScript(t, eventDir, "ok.sh", "#!/bin/sh\ncat >/dev/null\nexit 0\n")
	writeScript(t, eventDir, "fail.sh", "#!/bin/sh\ncat >/dev/null\necho boom 1>&2\nexit 7\n")

	disp := New(d, home)
	payload := map[string]any{"feature": map[string]any{"name": "x"}}
	if err := disp.Fire(context.Background(), EventPostFeatureSpawn, payload); err != nil {
		t.Fatalf("fire: %v", err)
	}

	rows, err := d.Query(`SELECT plugin, exit_code, stderr FROM hook_events ORDER BY plugin`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	type rec struct {
		plugin string
		exit   int
		stderr string
	}
	var got []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.plugin, &r.exit, &r.stderr); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(got), got)
	}
	if got[0].plugin != "fail.sh" || got[0].exit != 7 {
		t.Fatalf("fail.sh record wrong: %+v", got[0])
	}
	if got[1].plugin != "ok.sh" || got[1].exit != 0 {
		t.Fatalf("ok.sh record wrong: %+v", got[1])
	}
}

func TestFire_PayloadOnStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash scripts")
	}
	home := t.TempDir()
	d, err := db.Open(filepath.Join(home, "coda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	capture := filepath.Join(home, "captured.json")
	eventDir := filepath.Join(home, "hooks", string(EventPostOrchestratorStart))
	script := "#!/bin/sh\ncat > " + capture + "\n"
	writeScript(t, eventDir, "capture.sh", script)

	disp := New(d, home)
	if err := disp.Fire(context.Background(), EventPostOrchestratorStart,
		map[string]any{"orchestrator": map[string]any{"name": "ash"}}); err != nil {
		t.Fatalf("fire: %v", err)
	}

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal stdin: %v; raw=%s", err, data)
	}
	if got["event"] != string(EventPostOrchestratorStart) {
		t.Fatalf("event missing: %+v", got)
	}
	if _, ok := got["fired_at"]; !ok {
		t.Fatalf("fired_at missing: %+v", got)
	}
	orch, _ := got["orchestrator"].(map[string]any)
	if orch == nil || orch["name"] != "ash" {
		t.Fatalf("orchestrator missing/wrong: %+v", got)
	}
}
