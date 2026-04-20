# Copilot Cloud Agent Instructions for Coda

## What This Repository Is

Coda is a terminal-first orchestration tool for remote development servers. Its purpose is to make branch-based development easy to run in parallel by mapping git worktrees to persistent tmux sessions.

The central invariant — and the single most important thing to remember — is:

```
one active branch/worktree -> one tmux session
```

Every significant feature in this codebase exists to support or express that invariant. When in doubt about whether a change is correct, ask: does this strengthen or weaken the predictability of the branch/worktree/session model?

## Repository Structure

```
shell-functions.sh          Entry point: sources .env, sets defaults, loads lib/*.sh
lib/
  helpers.sh                Shared utilities: _coda_sanitize_session_name, _coda_detect_default_branch, _coda_find_project_root, _coda_load_project_config, _coda_resolve_effective_config
  core.sh                   coda() and coda-dev() entry points; attach/ls/switch/serve/help
  project.sh                Project lifecycle: start/add/workon/close/ls
  feature.sh                Feature branch lifecycle: start/done/finish/ls
  layout.sh                 Layout management: apply/ls/show/create
  provider.sh               Auth provider wiring: claude-auth, cliproxyapi
  profile.sh                Profile management: ls/create/show
  hooks.sh                  Lifecycle hook runner
  watch.sh                  Session watcher: start/stop/status
  mcp.sh                    MCP server management: start/stop/status/restart
  github.sh                 GitHub App integration (plugin-style)
  plugin.sh                 Plugin system: install/remove/update/ls and dynamic dispatch
hooks/                      Built-in lifecycle hook scripts (e.g. post-project-create)
cmd/coda-core/              Go companion binary (layout snapshot, provider, watcher)
mcp-server/                 Shared MCP HTTP server (port 3111, serves all OpenCode sessions)
tests/                      Shell lifecycle regression tests (bash, mock tmux/fzf)
  run.sh                    Test runner: bash tests/*.sh
  testlib.sh                Test helpers: assert_eq, assert_contains, assert_file_exists
  core-lifecycle.sh         Core lifecycle regression suite
  bin/                      Mock binaries used by tests (tmux, fzf)
test/                       Bats integration tests
  shell-modules.bats        Module loading tests
  tmux-integration.bats     Functional tmux integration tests
layouts/                    Built-in tmux layout scripts
docs/                       Design documents and ADRs
  coda-v1-design.md         Full product design brief
  coda-v1-branch-plan.md    Rewrite branch plan and milestones
  coda-v1-slice-01-core-lifecycle.md  First implementation slice spec
  adr/0001-core-orchestration-model.md
  adr/0002-layouts-and-customization-model.md
  adr/0003-companion-utilities-boundary.md
  plugin-contracts/         Plugin API documentation (commands, hooks, providers, notifications, layouts)
```

## Key Design Decisions

Always read the ADRs before making significant changes:

- **ADR-0001**: Core is the branch/worktree/session orchestration layer. Session naming conventions are architecture, not cosmetics.
- **ADR-0002**: Customization uses config → profiles → lifecycle hooks. No broad plugin API in v1.
- **ADR-0003**: Machine-specific bootstrap, auth bridges, and watcher delivery are companion utilities — they stay in the repo but must not dictate core architecture.

## How the Code Works

### Loading chain

`shell-functions.sh` is sourced in `.bashrc`/`.zshrc`. It:
1. Sources `.env` (global config) and sets defaults for all `CODA_*` / `PROJECTS_DIR` / `SESSION_PREFIX` env vars.
2. Sources all `lib/*.sh` modules in order.
3. Calls `_coda_plugin_load_all` to register any installed plugins.
4. Optionally auto-attaches tmux on SSH login if `AUTO_ATTACH_TMUX=true`.

### Session naming

Session names are derived deterministically, but note that the implementation sanitizes the full session name string at attach time rather than always sanitizing project and branch separately first:
- Project session: typically `${SESSION_PREFIX}<project>`, then `_coda_sanitize_session_name(...)` is applied to the whole name (e.g. `coda-myapp`)
- Feature session: typically `${SESSION_PREFIX}<project>--<branch>`, then `_coda_sanitize_session_name(...)` is applied to that whole name (e.g. `coda-myapp--auth-flow`)
- Sanitization: `_coda_sanitize_session_name` replaces `.`, `/`, ` `, `:` with `-`.
- Important mismatch: `lib/feature.sh` teardown logic currently computes the feature session name from the raw branch, so do not assume `${sanitized_branch}` is used consistently everywhere session names are reconstructed.

### Project layout on disk

A coda project at `$PROJECTS_DIR/<name>/` looks like:
```
<name>/
  .bare/             git objects (bare repo)
  .git               text file: "gitdir: ./.bare"
  <default-branch>/  worktree for the repository's default branch
  <branch>/          worktree for each active non-default feature branch
  .coda.env          optional per-project config overrides
```

`_coda_find_project_root` walks up the directory tree looking for this `.bare` + `.git` pattern.

### Configuration precedence (lowest → highest)

1. Built-in defaults in `shell-functions.sh`
2. Global `.env` file
3. Per-project `.coda.env`
4. Profile values
5. Environment variables
6. Explicit CLI flags (`--layout`, `--profile`)

### MCP server

`mcp-server/server.js` defaults to running as a single shared StreamableHTTP server on `CODA_MCP_PORT` (default 3111), and all OpenCode sessions connect to that shared server. It also supports a stdio transport when launched with `--stdio`. It sources `shell-functions.sh` and calls `coda` subcommands as MCP tools. For the shared HTTP server, run with `coda mcp start|stop|status|restart`.

**If you add or rename a `coda` subcommand, you must update the corresponding tool in `mcp-server/server.js`.**

Core shell → MCP tool mapping:

| Shell command | MCP tool |
|---|---|
| `coda ls` | `coda_ls` |
| `coda project ls` | `coda_project_ls` |
| `coda project start --repo` | `coda_project_clone` |
| `coda project start --new` | `coda_project_create` |
| `coda project workon` | `coda_project_workon` |
| `coda project close` | `coda_project_close` |
| `coda feature ls` | `coda_feature_ls` |
| `coda feature start` | `coda_feature_start` |
| `coda feature done` | `coda_feature_done` |
| `coda feature finish` | `coda_feature_finish` |
| `coda layout ls` | `coda_layout_ls` |
| `coda layout show` | `coda_layout_show` |
| `coda help` | `coda_help` |

### Plugin system

Plugins are git repos installed to `$CODA_PLUGINS_DIR/<name>/`. Each has a `plugin.json` manifest that can declare:
- `provides.commands` — new `coda <subcmd>` commands (sourced shell functions)
- `provides.hooks` — lifecycle hook scripts
- `provides.providers` — auth providers
- `provides.notifications` — notification scripts
- `provides.mcp_tools` — additional MCP tools

Plugin management: `coda plugin install|remove|update|ls`.

## How to Run Tests

### Shell regression tests (primary)
```bash
bash tests/run.sh
```
This runs all `tests/*.sh` files except `run.sh` and `testlib.sh`. Each test file sources `shell-functions.sh` with mock environment overrides. Mock tmux and fzf binaries live in `tests/bin/`.

### Bats integration tests
```bash
# Requires bats-core to be installed
bats test/
```

### Go binary
```bash
cd cmd/coda-core
go build ./...
go test ./...
```

### MCP server
```bash
cd mcp-server
npm test
```

## What To Do Before Making Changes

1. Read `AGENTS.md` — it defines what Coda is, what it is not, and the decision rules agents should follow.
2. Check if a relevant ADR exists in `docs/adr/`.
3. Run `bash tests/run.sh` to confirm the baseline is green before editing.
4. Run tests again after editing to confirm nothing regressed.

## What Changes Belong Where

| Type of change | Location |
|---|---|
| Session naming, attach/list/switch logic | `lib/core.sh` |
| Project clone/create/reconnect/close | `lib/project.sh` |
| Feature branch create/teardown | `lib/feature.sh` |
| Layout application | `lib/layout.sh` |
| Config resolution, path helpers | `lib/helpers.sh` |
| Hook execution | `lib/hooks.sh` |
| Auth provider wiring | `lib/provider.sh` |
| MCP server tool definitions | `mcp-server/server.js` |
| Built-in layout scripts | `layouts/` |
| Built-in lifecycle hook scripts | `hooks/` |
| Core lifecycle regression tests | `tests/core-lifecycle.sh` |
| Go layout/provider/watcher tooling | `cmd/coda-core/` |

**Do not** put machine-bootstrap logic, device-specific helpers, or provider-auth details into `lib/core.sh`, `lib/project.sh`, or `lib/feature.sh`. Those belong in companion scripts or provider modules.

## Common Patterns

### Adding a new subcommand
1. Add the function to the appropriate `lib/*.sh` file.
2. Add a `case` entry in `coda()` in `lib/core.sh`.
3. Add a matching MCP tool entry in `mcp-server/server.js`.
4. Add a test in `tests/`.

### Adding a lifecycle hook point
1. Call `_coda_run_hooks <event-name>` from the appropriate location in `lib/project.sh` or `lib/feature.sh`.
2. Document the hook event name and available environment variables in `docs/plugin-contracts/hooks.md`.
3. Optionally add a built-in hook script in `hooks/`.

### Adding a config variable
1. Add it with a default value in `shell-functions.sh`.
2. Add it with documentation to `.env.example`.
3. If it is project-level, document it in the `.coda.env` section of `.env.example`.

## Current Branch Context

This repository is on a **rewrite branch** (`coda-v1`). `main` is the stable production line; this branch is building the future canonical version.

- Use `coda-dev` (not `coda`) when validating the rewrite alongside an existing `coda` installation — it uses `CODA_DEV_SESSION_PREFIX` to avoid session collisions.
- The first proving slice is `docs/coda-v1-slice-01-core-lifecycle.md` — the core lifecycle spine.
- Do not expand adjacent utilities (bootstrap, auth, watcher) before the core lifecycle tests are green.
- Do not merge rewrite work into `main` until the end-to-end core path is proven.

## Decision Rule

Before making any change, ask:
1. Does this strengthen the branch/worktree/session model?
2. Does this make the core easier to reason about or test?
3. Does this belong in core, or is it a companion concern?

If unclear on all three, stop and ask for clarification instead of inventing behavior.
