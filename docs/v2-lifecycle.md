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

## Build

```
make coda-core      # builds ./coda-core at repo root (gitignored)
make test           # runs Go tests + shell tests
```

`install.sh` builds the binary to `~/.local/bin/coda-core` when Go is
available; without Go the v1 CLI still works fully.
