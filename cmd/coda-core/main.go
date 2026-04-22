package main

import (
	"errors"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "layout":
		err = runLayout(os.Args[2:])
	case "provider":
		err = runProvider(os.Args[2:])
	case "watch":
		err = runWatch(os.Args[2:])
	case "github":
		err = runGitHub(os.Args[2:])
	case "orchestrator":
		err = runOrchestrator(os.Args[2:])
	case "feature":
		err = runFeature(os.Args[2:])
	case "status":
		err = runStatus(os.Args[2:])
	case "version", "--version", "-V":
		err = runVersion(os.Args[2:])
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		var cce *codaCoreError
		if errors.As(err, &cce) {
			os.Exit(cce.code)
		}
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `coda-core — internal helper for the coda shell functions

Usage: coda-core <command> [args...]

Commands:
  layout snapshot    Capture current tmux layout as a script or YAML config
  layout render      Render a YAML layout config as bash functions
  provider auth      Configure CLIProxyAPI provider in OpenCode config
  provider status    Show provider diagnostics
  watch              Monitor OpenCode sessions and notify on attention needed
  github token       Generate a GitHub App installation access token
  github comment     Post a comment as the GitHub App identity

v2 lifecycle (SQLite-backed):
  orchestrator new     Register an orchestrator
  orchestrator start   Mark orchestrator running
  orchestrator stop    Mark orchestrator stopped
  orchestrator ls      List orchestrators
  orchestrator rm      Remove an orchestrator
  feature spawn        Register a feature (state=spawning)
  feature ls           List features
  feature attach       Mark feature running
  feature finish       Mark feature done (fires pre-feature-teardown)
  status               Combined orchestrator + feature status
  version              Print version + resolved CODA_HOME / DB`)
}
