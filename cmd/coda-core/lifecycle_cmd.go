package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/evanstern/coda/internal/db"
	"github.com/evanstern/coda/internal/hooks"
	"github.com/evanstern/coda/internal/lifecycle"
)

const codaCoreVersion = "0.1.0-dev"

// Exit code contract. See docs/v2-lifecycle.md. Stable across v2 releases.
const (
	ExitSuccess          = 0
	ExitUserError        = 1
	ExitDBError          = 2
	ExitLifecycleBlocked = 3
)

type codaCoreError struct {
	code int
	msg  string
}

func (e *codaCoreError) Error() string { return e.msg }

func userError(f string, a ...any) error {
	return &codaCoreError{code: ExitUserError, msg: fmt.Sprintf(f, a...)}
}

func dbError(err error) error {
	return &codaCoreError{code: ExitDBError, msg: err.Error()}
}

// parseInterleaved accepts flags anywhere in args (stdlib flag only
// scans until the first non-flag token). It reorders args so flags come
// first, preserving the order of positionals.
func parseInterleaved(fs *flag.FlagSet, args []string) error {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && flagTakesValue(fs, a) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return fs.Parse(append(flags, positional...))
}

func flagTakesValue(fs *flag.FlagSet, name string) bool {
	n := name
	for len(n) > 0 && n[0] == '-' {
		n = n[1:]
	}
	f := fs.Lookup(n)
	if f == nil {
		return false
	}
	if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
		return false
	}
	return true
}

func openManager() (*sql.DB, *lifecycle.Manager, string, error) {
	home, err := db.DefaultHome()
	if err != nil {
		return nil, nil, "", err
	}
	path, err := db.DefaultPath()
	if err != nil {
		return nil, nil, "", err
	}
	d, err := db.Open(path)
	if err != nil {
		return nil, nil, "", err
	}
	disp := hooks.New(d, home)
	return d, lifecycle.New(d, disp), path, nil
}

func runVersion(args []string) error {
	home, err := db.DefaultHome()
	if err != nil {
		return err
	}
	path, err := db.DefaultPath()
	if err != nil {
		return err
	}
	fmt.Printf("coda-core %s\nCODA_HOME=%s\nDB=%s\n", codaCoreVersion, home, path)
	return nil
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := parseInterleaved(fs, args); err != nil {
		return userError("%v", err)
	}

	d, mgr, _, err := openManager()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()

	ctx := context.Background()
	orchs, err := mgr.ListOrchestrators(ctx)
	if err != nil {
		return dbError(err)
	}
	feats, err := mgr.ListFeatures(ctx, "")
	if err != nil {
		return dbError(err)
	}

	if *asJSON {
		out := map[string]any{
			"orchestrators": orchsToJSON(orchs),
			"features":      featuresToJSON(feats),
		}
		return writeJSON(os.Stdout, out)
	}

	printOrchTable(os.Stdout, orchs)
	fmt.Fprintln(os.Stdout)
	printFeatureTable(os.Stdout, feats)
	return nil
}

func runOrchestrator(args []string) error {
	if len(args) == 0 {
		return userError("usage: coda-core orchestrator <new|start|stop|ls|rm> ...")
	}
	switch args[0] {
	case "new":
		return orchNew(args[1:])
	case "start":
		return orchStart(args[1:])
	case "stop":
		return orchStop(args[1:])
	case "ls":
		return orchLs(args[1:])
	case "rm":
		return orchRm(args[1:])
	default:
		return userError("unknown orchestrator subcommand: %s", args[0])
	}
}

func orchNew(args []string) error {
	fs := flag.NewFlagSet("orchestrator new", flag.ContinueOnError)
	configDir := fs.String("config-dir", "", "config dir for the orchestrator")
	if err := parseInterleaved(fs, args); err != nil {
		return userError("%v", err)
	}
	if fs.NArg() != 1 {
		return userError("usage: coda-core orchestrator new <name> --config-dir <path>")
	}
	if *configDir == "" {
		return userError("--config-dir is required")
	}
	name := fs.Arg(0)

	d, mgr, _, err := openManager()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()

	o, err := mgr.CreateOrchestrator(context.Background(), name, *configDir)
	if err != nil {
		if errors.Is(err, lifecycle.ErrExists) {
			return userError("%v", err)
		}
		return dbError(err)
	}
	fmt.Printf("created orchestrator %q (state=%s)\n", o.Name, o.State)
	return nil
}

func orchStart(args []string) error {
	fs := flag.NewFlagSet("orchestrator start", flag.ContinueOnError)
	tmuxSession := fs.String("tmux-session", "", "tmux session name (optional)")
	port := fs.Int("port", 0, "listener port (optional)")
	pid := fs.Int("pid", 0, "process id (optional)")
	if err := parseInterleaved(fs, args); err != nil {
		return userError("%v", err)
	}
	if fs.NArg() != 1 {
		return userError("usage: coda-core orchestrator start <name> [--tmux-session ...] [--port N] [--pid N]")
	}
	name := fs.Arg(0)

	d, mgr, _, err := openManager()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()

	o, err := mgr.StartOrchestrator(context.Background(), name, *tmuxSession, *port, *pid)
	if err != nil {
		if errors.Is(err, lifecycle.ErrNotFound) {
			return userError("%v", err)
		}
		return dbError(err)
	}
	fmt.Printf("started orchestrator %q (state=%s)\n", o.Name, o.State)
	return nil
}

func orchStop(args []string) error {
	if len(args) != 1 {
		return userError("usage: coda-core orchestrator stop <name>")
	}
	d, mgr, _, err := openManager()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()
	o, err := mgr.StopOrchestrator(context.Background(), args[0])
	if err != nil {
		if errors.Is(err, lifecycle.ErrNotFound) {
			return userError("%v", err)
		}
		return dbError(err)
	}
	fmt.Printf("stopped orchestrator %q (state=%s)\n", o.Name, o.State)
	return nil
}

func orchLs(args []string) error {
	fs := flag.NewFlagSet("orchestrator ls", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := parseInterleaved(fs, args); err != nil {
		return userError("%v", err)
	}
	d, mgr, _, err := openManager()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()

	orchs, err := mgr.ListOrchestrators(context.Background())
	if err != nil {
		return dbError(err)
	}
	if *asJSON {
		return writeJSON(os.Stdout, orchsToJSON(orchs))
	}
	printOrchTable(os.Stdout, orchs)
	return nil
}

func orchRm(args []string) error {
	if len(args) != 1 {
		return userError("usage: coda-core orchestrator rm <name>")
	}
	d, mgr, _, err := openManager()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()
	if err := mgr.RemoveOrchestrator(context.Background(), args[0]); err != nil {
		if errors.Is(err, lifecycle.ErrNotFound) {
			return userError("%v", err)
		}
		return dbError(err)
	}
	fmt.Printf("removed orchestrator %q\n", args[0])
	return nil
}

func runFeature(args []string) error {
	if len(args) == 0 {
		return userError("usage: coda-core feature <spawn|ls|attach|finish> ...")
	}
	switch args[0] {
	case "spawn":
		return featureSpawn(args[1:])
	case "ls":
		return featureLs(args[1:])
	case "attach":
		return featureAttach(args[1:])
	case "finish":
		return featureFinish(args[1:])
	default:
		return userError("unknown feature subcommand: %s", args[0])
	}
}

func featureSpawn(args []string) error {
	fs := flag.NewFlagSet("feature spawn", flag.ContinueOnError)
	orch := fs.String("orch", "", "orchestrator name (required)")
	project := fs.String("project", "", "project name (required)")
	branch := fs.String("branch", "", "branch name (required)")
	worktree := fs.String("worktree", "", "worktree dir (required)")
	brief := fs.String("brief", "", "brief path (optional)")
	name := fs.String("name", "", "feature name (defaults to branch)")
	if err := parseInterleaved(fs, args); err != nil {
		return userError("%v", err)
	}
	if *orch == "" || *project == "" || *branch == "" || *worktree == "" {
		return userError("feature spawn requires --orch, --project, --branch, --worktree")
	}

	d, mgr, _, err := openManager()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()

	f, err := mgr.SpawnFeature(context.Background(), lifecycle.SpawnFeatureInput{
		OrchestratorName: *orch,
		Name:             *name,
		Project:          *project,
		Branch:           *branch,
		WorktreeDir:      *worktree,
		BriefPath:        *brief,
	})
	if err != nil {
		if errors.Is(err, lifecycle.ErrExists) || errors.Is(err, lifecycle.ErrNotFound) {
			return userError("%v", err)
		}
		return dbError(err)
	}
	fmt.Printf("spawned feature %q on orchestrator %q (state=%s)\n", f.Name, *orch, f.State)
	return nil
}

func featureLs(args []string) error {
	fs := flag.NewFlagSet("feature ls", flag.ContinueOnError)
	orch := fs.String("orch", "", "filter by orchestrator (optional)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := parseInterleaved(fs, args); err != nil {
		return userError("%v", err)
	}
	d, mgr, _, err := openManager()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()
	feats, err := mgr.ListFeatures(context.Background(), *orch)
	if err != nil {
		return dbError(err)
	}
	if *asJSON {
		return writeJSON(os.Stdout, featuresToJSON(feats))
	}
	printFeatureTable(os.Stdout, feats)
	return nil
}

func featureAttach(args []string) error {
	fs := flag.NewFlagSet("feature attach", flag.ContinueOnError)
	orch := fs.String("orch", "", "orchestrator name (required)")
	if err := parseInterleaved(fs, args); err != nil {
		return userError("%v", err)
	}
	if fs.NArg() != 1 || *orch == "" {
		return userError("usage: coda-core feature attach --orch <name> <feature>")
	}
	d, mgr, _, err := openManager()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()
	f, err := mgr.AttachFeature(context.Background(), *orch, fs.Arg(0))
	if err != nil {
		if errors.Is(err, lifecycle.ErrNotFound) {
			return userError("%v", err)
		}
		return dbError(err)
	}
	fmt.Printf("attached feature %q (state=%s)\n", f.Name, f.State)
	return nil
}

func featureFinish(args []string) error {
	fs := flag.NewFlagSet("feature finish", flag.ContinueOnError)
	orch := fs.String("orch", "", "orchestrator name (required)")
	if err := parseInterleaved(fs, args); err != nil {
		return userError("%v", err)
	}
	if fs.NArg() != 1 || *orch == "" {
		return userError("usage: coda-core feature finish --orch <name> <feature>")
	}
	d, mgr, _, err := openManager()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()
	f, err := mgr.FinishFeature(context.Background(), *orch, fs.Arg(0))
	if err != nil {
		if errors.Is(err, lifecycle.ErrNotFound) {
			return userError("%v", err)
		}
		return dbError(err)
	}
	fmt.Printf("finished feature %q (state=%s)\n", f.Name, f.State)
	return nil
}

func orchsToJSON(orchs []*lifecycle.Orchestrator) []map[string]any {
	out := make([]map[string]any, 0, len(orchs))
	for _, o := range orchs {
		m := map[string]any{
			"name":       o.Name,
			"config_dir": o.ConfigDir,
			"state":      o.State,
			"created_at": o.CreatedAt,
			"updated_at": o.UpdatedAt,
		}
		if o.TmuxSession.Valid {
			m["tmux_session"] = o.TmuxSession.String
		}
		if o.Port.Valid {
			m["port"] = o.Port.Int64
		}
		if o.PID.Valid {
			m["pid"] = o.PID.Int64
		}
		if o.StartedAt.Valid {
			m["started_at"] = o.StartedAt.Int64
		}
		out = append(out, m)
	}
	return out
}

func featuresToJSON(feats []*lifecycle.Feature) []map[string]any {
	out := make([]map[string]any, 0, len(feats))
	for _, f := range feats {
		m := map[string]any{
			"name":            f.Name,
			"orchestrator_id": f.OrchestratorID,
			"project":         f.Project,
			"branch":          f.Branch,
			"worktree_dir":    f.WorktreeDir,
			"state":           f.State,
			"created_at":      f.CreatedAt,
			"updated_at":      f.UpdatedAt,
		}
		if f.TmuxSession.Valid {
			m["tmux_session"] = f.TmuxSession.String
		}
		if f.BriefPath.Valid {
			m["brief_path"] = f.BriefPath.String
		}
		if f.PRURL.Valid {
			m["pr_url"] = f.PRURL.String
		}
		if f.EndedAt.Valid {
			m["ended_at"] = f.EndedAt.Int64
		}
		out = append(out, m)
	}
	return out
}

func printOrchTable(w io.Writer, orchs []*lifecycle.Orchestrator) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tTMUX\tPORT\tPID\tCONFIG_DIR")
	for _, o := range orchs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			o.Name, o.State,
			nullStrOr(o.TmuxSession, "-"),
			nullIntOr(o.Port, "-"),
			nullIntOr(o.PID, "-"),
			o.ConfigDir)
	}
	if len(orchs) == 0 {
		fmt.Fprintln(tw, "(no orchestrators)")
	}
	tw.Flush()
}

func printFeatureTable(w io.Writer, feats []*lifecycle.Feature) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tORCH_ID\tPROJECT\tBRANCH\tSTATE\tWORKTREE")
	for _, f := range feats {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n",
			f.Name, f.OrchestratorID, f.Project, f.Branch, f.State, f.WorktreeDir)
	}
	if len(feats) == 0 {
		fmt.Fprintln(tw, "(no features)")
	}
	tw.Flush()
}

func nullStrOr(s sql.NullString, fallback string) string {
	if s.Valid && s.String != "" {
		return s.String
	}
	return fallback
}

func nullIntOr(n sql.NullInt64, fallback string) string {
	if n.Valid {
		return fmt.Sprintf("%d", n.Int64)
	}
	return fallback
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
