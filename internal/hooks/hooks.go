// Package hooks implements v2 hook discovery and dispatch. Hook scripts
// live at $CODA_HOME/hooks/<event>/*.sh. Each script is executed with the
// typed JSON payload on stdin; the exit code and stderr are captured and
// recorded in the hook_events table. Hooks are always non-fatal in this
// card (the fatal=true manifest flag is a #150 concern).
package hooks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// Event is the name of a hook point.
type Event string

const (
	EventPostOrchestratorStart Event = "post-orchestrator-start"
	EventPostOrchestratorStop  Event = "post-orchestrator-stop"
	EventPostFeatureSpawn      Event = "post-feature-spawn"
	EventPreFeatureTeardown    Event = "pre-feature-teardown"
)

// Dispatcher fires hooks for lifecycle events.
type Dispatcher struct {
	DB      *sql.DB
	HomeDir string
	Now     func() time.Time
}

// New returns a Dispatcher reading scripts from <homeDir>/hooks/<event>/.
func New(db *sql.DB, homeDir string) *Dispatcher {
	return &Dispatcher{DB: db, HomeDir: homeDir, Now: time.Now}
}

// Fire runs every executable script under $CODA_HOME/hooks/<event>/,
// piping the JSON payload on stdin and recording the result. Errors from
// individual scripts do not stop the caller; they are logged to
// hook_events. Fire only returns an error for infrastructure failures
// (DB write errors, payload marshal errors).
func (d *Dispatcher) Fire(ctx context.Context, event Event, payload map[string]any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["event"] = string(event)
	firedAt := d.Now().Unix()
	payload["fired_at"] = firedAt

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	scripts, err := d.scripts(event)
	if err != nil {
		return fmt.Errorf("discover scripts: %w", err)
	}

	for _, s := range scripts {
		exitCode, stderr := d.runOne(ctx, s, data)
		plugin := filepath.Base(s)
		if _, err := d.DB.ExecContext(ctx,
			`INSERT INTO hook_events (event, plugin, payload, exit_code, stderr, fired_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			string(event), plugin, string(data), exitCode, stderr, firedAt,
		); err != nil {
			return fmt.Errorf("record hook_event: %w", err)
		}
	}
	return nil
}

func (d *Dispatcher) scripts(event Event) ([]string, error) {
	dir := filepath.Join(d.HomeDir, "hooks", string(event))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".sh" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Mode()&0o111 == 0 {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

func (d *Dispatcher) runOne(ctx context.Context, script string, payload []byte) (int, string) {
	cmd := exec.CommandContext(ctx, script)
	cmd.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), "CODA_HOME="+d.HomeDir)

	err := cmd.Run()
	if err == nil {
		return 0, stderr.String()
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), stderr.String()
	}
	return -1, err.Error() + "\n" + stderr.String()
}
