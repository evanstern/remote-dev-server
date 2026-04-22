# v2 lifecycle (coda-core)

`coda-core` is a Go binary that provides v2 lifecycle commands backed by a
SQLite state store at `$CODA_HOME/coda.db` (default
`~/.local/state/coda/coda.db`). It coexists with the existing bash CLI:
all v1 verbs continue to work via `lib/*.sh`, and v2 verbs are dispatched
to `coda-core` when it is on `PATH`.

## Coexistence model

| Command                          | Handler     | Notes                                   |
|----------------------------------|-------------|-----------------------------------------|
| `coda feature start <branch>`    | bash (v1)   | Unchanged — creates worktree + session. |
| `coda feature done <branch>`     | bash (v1)   | Unchanged.                              |
| `coda feature finish [--force]`  | bash (v1)   | Unchanged.                              |
| `coda feature ls`                | bash (v1)   | Stays on bash for now.                  |
| `coda feature spawn ...`         | `coda-core` | v2 — records feature in state store.    |
| `coda feature attach ...`        | `coda-core` | v2.                                     |
| `coda orch ...`                  | bash (v1)   | Unchanged (if provided by a plugin).    |
| `coda orchestrator ...`          | `coda-core` | v2 — orchestrator CRUD.                 |
| `coda status`                    | `coda-core` | v2 — combined orchestrator + feature report. |
| `coda version`                   | bash (v1)   | Unchanged — prints CODA_VERSION.        |
| `coda-core version`              | `coda-core` | Prints binary version + CODA_HOME + DB. |

The bash dispatcher routes `orchestrator`, `status`, `feature spawn`, and
`feature attach` to `coda-core`. Other `feature` subcommands continue to
route to `lib/feature.sh` as before.

If a v2 verb is invoked while `coda-core` is not on `PATH`, the dispatcher
prints a build hint:

```
coda-core binary not found on PATH.
Build with 'make coda-core' and install, or rerun ./install.sh.
```

## v1/v2 feature routing (transition state)

In #148, `coda feature start/done/finish/ls` stay on the v1 bash path.
`coda feature spawn/attach` route to v2 `coda-core`. These do not share
state:

- A feature created via `coda feature start` is a worktree + tmux session
  with **no row** in `coda.db`. It will NOT appear in `coda status` or
  `coda-core feature ls`.
- A feature created via `coda feature spawn` is a row in `coda.db` with
  **no worktree or tmux session** — v2 is state-only in #148.

`coda status` queries the DB only and therefore shows v2 features only.
This split is resolved structurally in #151 (migration). Until then,
users should not mix v1 and v2 feature commands on the same feature.

## State store

Three tables live in `$CODA_HOME/coda.db`:

- `orchestrators` — one row per registered orchestrator (name unique).
- `features` — one row per spawned feature, `ON DELETE CASCADE` via `orchestrator_id`.
- `hook_events` — append-only log of every hook invocation and its result.

The schema is embedded in `internal/db/schema.sql` and applied
idempotently on open. WAL + foreign keys are enabled.

## Hooks

Hook scripts are discovered at `$CODA_HOME/hooks/<event>/*.sh` (executable
files only, sorted). Each hook receives a typed JSON payload on stdin and
its exit code + stderr is recorded in `hook_events`. Hooks are always
non-fatal in v2.0 — the `fatal=true` manifest flag is a v2.1 plugin-system
concern.

Events implemented by `coda-core`:

- `post-orchestrator-start`
- `post-orchestrator-stop`
- `post-feature-spawn`
- `pre-feature-teardown` — fires **before** the row is marked `done`, so
  hooks observing the DB see state=`running`.

## Hook dispatch ordering

When multiple hooks subscribe to the same event, they fire in this order:

1. **Plugin-declared hooks first** (from `plugin.toml` manifests, via the
   plugin registry — this layer ships with #150).
2. **Filesystem hooks second** (scripts at `$CODA_HOME/hooks/<event>/*.sh`,
   discovered in lexical order by filename).
3. **Fatal wins**: if any hook declared `fatal = true` exits non-zero,
   the lifecycle transition is blocked regardless of subsequent hooks.
   Hooks not marked fatal are non-fatal — their failures are logged to
   `hook_events` and ignored for transition purposes.

In #148, only filesystem hooks exist and all are non-fatal. The
plugin-first rule takes effect when #150 ships. This ordering is the
binding contract for plugin authors.

Exit codes from hooks are recorded in `hook_events.exit_code`. A
fatal-hook block surfaces to the caller as coda-core exit code 3
(reserved; see Exit codes below).

## Exit codes (coda-core)

| Code | Name                  | Meaning                                    |
|------|-----------------------|--------------------------------------------|
| 0    | success               | Operation completed.                       |
| 1    | user error            | Bad args, not found, duplicate, etc.       |
| 2    | DB error              | Schema or SQLite failure.                  |
| 3    | lifecycle blocked     | Reserved. Returned when a `fatal=true`     |
|      |                       | hook blocks a transition (#150).           |

Callers MUST treat any non-zero exit as an error. Exit code 3 signals
"user action required, do not retry" and is stable across v2 releases
even though #148 does not yet emit it.

## Build

```
make coda-core      # builds ./coda-core at repo root (gitignored)
make test           # runs Go tests + shell tests
```

`install.sh` builds the binary to `~/.local/bin/coda-core` when Go is
available; without Go the v1 CLI still works fully.
