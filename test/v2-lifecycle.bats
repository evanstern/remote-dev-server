#!/usr/bin/env bats

# Integration tests for v2 lifecycle: coda-core binary + bash routing.

SCRIPT_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"

setup() {
    export CODA_HOME="$(mktemp -d)"
    export AUTO_ATTACH_TMUX=false
    export SSH_CONNECTION=""
    export CODA_SKIP_ENV=true
    if [ ! -x "$SCRIPT_DIR/coda-core" ]; then
        skip "coda-core not built — run 'make coda-core'"
    fi
    # Isolate the binary in a dedicated bin dir so PATH-based routing
    # tests don't pick up a stale ~/.local/bin/coda-core.
    TEST_BIN_DIR="$(mktemp -d)"
    cp "$SCRIPT_DIR/coda-core" "$TEST_BIN_DIR/coda-core"
    CODA_CORE_BIN="$TEST_BIN_DIR/coda-core"
}

teardown() {
    [ -n "${CODA_HOME:-}" ] && rm -rf "$CODA_HOME"
    [ -n "${TEST_BIN_DIR:-}" ] && rm -rf "$TEST_BIN_DIR"
}

@test "v2: coda-core version prints non-empty output" {
    run "$CODA_CORE_BIN" version
    [ "$status" -eq 0 ]
    [[ "$output" == *"coda-core"* ]]
    [[ "$output" == *"CODA_HOME"* ]]
}

@test "v2: orchestrator new + ls roundtrip" {
    run "$CODA_CORE_BIN" orchestrator new alice --config-dir /tmp/alice
    [ "$status" -eq 0 ]

    run "$CODA_CORE_BIN" orchestrator ls
    [ "$status" -eq 0 ]
    [[ "$output" == *"alice"* ]]
    [[ "$output" == *"stopped"* ]]
}

@test "v2: orchestrator ls --json emits JSON array" {
    "$CODA_CORE_BIN" orchestrator new alice --config-dir /tmp/alice
    run "$CODA_CORE_BIN" orchestrator ls --json
    [ "$status" -eq 0 ]
    [[ "$output" == *'"name": "alice"'* ]]
}

@test "v2: orchestrator new duplicate fails with exit 1" {
    "$CODA_CORE_BIN" orchestrator new alice --config-dir /tmp/alice
    run "$CODA_CORE_BIN" orchestrator new alice --config-dir /tmp/alice
    [ "$status" -eq 1 ]
    [[ "$output" == *"already exists"* ]]
}

@test "v2: orchestrator start then stop transitions state" {
    "$CODA_CORE_BIN" orchestrator new alice --config-dir /tmp/alice
    run "$CODA_CORE_BIN" orchestrator start alice --port 4096 --pid 1234
    [ "$status" -eq 0 ]
    [[ "$output" == *"running"* ]]

    run "$CODA_CORE_BIN" orchestrator stop alice
    [ "$status" -eq 0 ]
    [[ "$output" == *"stopped"* ]]
}

@test "v2: feature spawn then finish" {
    "$CODA_CORE_BIN" orchestrator new alice --config-dir /tmp/alice
    run "$CODA_CORE_BIN" feature spawn --orch alice --project coda --branch auth --worktree /tmp/wt
    [ "$status" -eq 0 ]
    [[ "$output" == *"spawned"* ]]

    run "$CODA_CORE_BIN" feature finish --orch alice auth
    [ "$status" -eq 0 ]
    [[ "$output" == *"done"* ]]
}

@test "v2: feature spawn against missing orchestrator errors cleanly" {
    run "$CODA_CORE_BIN" feature spawn --orch ghost --project p --branch b --worktree /tmp/wt
    [ "$status" -eq 1 ]
    [[ "$output" == *"not found"* ]]
}

@test "v2: status command shows orchestrators and features" {
    "$CODA_CORE_BIN" orchestrator new alice --config-dir /tmp/alice
    "$CODA_CORE_BIN" feature spawn --orch alice --project coda --branch auth --worktree /tmp/wt

    run "$CODA_CORE_BIN" status
    [ "$status" -eq 0 ]
    [[ "$output" == *"alice"* ]]
    [[ "$output" == *"auth"* ]]
}

@test "v2: DB file is created under CODA_HOME" {
    "$CODA_CORE_BIN" version >/dev/null
    # A simple command that opens the db
    "$CODA_CORE_BIN" orchestrator ls >/dev/null
    [ -f "$CODA_HOME/coda.db" ]
}

@test "v2 routing: bash coda orchestrator errors when coda-core missing from PATH" {
    # Sanitized PATH with no coda-core anywhere. Disable plugin loading
    # so it can't re-prepend ~/.local/bin (where a stale binary may live).
    run env -i HOME="$HOME" PATH="/usr/bin:/bin" \
        CODA_PLUGINS_DIR="$TEST_BIN_DIR/empty-plugins" \
        bash -c '
        export AUTO_ATTACH_TMUX=false SSH_CONNECTION="" CODA_SKIP_ENV=true
        source "'"$SCRIPT_DIR"'/shell-functions.sh"
        coda orchestrator ls
    '
    [ "$status" -ne 0 ]
    [[ "$output" == *"coda-core binary not found"* ]]
}

@test "v2 routing: bash coda orchestrator ls delegates to coda-core" {
    "$CODA_CORE_BIN" orchestrator new delegated --config-dir /tmp/x

    run env -i HOME="$HOME" PATH="$TEST_BIN_DIR:/usr/bin:/bin" CODA_HOME="$CODA_HOME" \
        CODA_PLUGINS_DIR="$TEST_BIN_DIR/empty-plugins" \
        bash -c '
        export AUTO_ATTACH_TMUX=false SSH_CONNECTION="" CODA_SKIP_ENV=true
        source "'"$SCRIPT_DIR"'/shell-functions.sh"
        coda orchestrator ls
    '
    [ "$status" -eq 0 ]
    [[ "$output" == *"delegated"* ]]
}

@test "v2 routing: bash coda feature start still routes to bash (v1)" {
    # Shadow coda-core on PATH with a sentinel binary. If bash routing
    # leaks `feature start` to coda-core, the sentinel runs and we detect
    # it via exit code 42 and the sentinel string. v1 routing should
    # never invoke coda-core for `feature start`.
    SHADOW_BIN_DIR="$(mktemp -d)"
    cat >"$SHADOW_BIN_DIR/coda-core" <<'SENTINEL'
#!/usr/bin/env bash
echo "UNEXPECTED_CODA_CORE_INVOCATION"
exit 42
SENTINEL
    chmod +x "$SHADOW_BIN_DIR/coda-core"

    run env -i HOME="$HOME" PATH="$SHADOW_BIN_DIR:/usr/bin:/bin" \
        CODA_PLUGINS_DIR="$TEST_BIN_DIR/empty-plugins" \
        bash -c '
        export AUTO_ATTACH_TMUX=false SSH_CONNECTION="" CODA_SKIP_ENV=true
        source "'"$SCRIPT_DIR"'/shell-functions.sh"
        coda feature start 2>/dev/null
        true
    '
    rm -rf "$SHADOW_BIN_DIR"

    [ "$status" -ne 42 ]
    [[ "$output" != *"UNEXPECTED_CODA_CORE_INVOCATION"* ]]
}

@test "v2: orchestrator reconcile on empty DB prints 'no rows'" {
    run "$CODA_CORE_BIN" orchestrator reconcile
    [ "$status" -eq 0 ]
    [[ "$output" == *"no rows transitioned"* ]]
}

backdate_orch() {
    command -v sqlite3 >/dev/null || skip "sqlite3 CLI not installed"
    sqlite3 "$CODA_HOME/coda.db" "UPDATE orchestrators SET updated_at = updated_at - 120 WHERE name='$1';"
}

@test "v2: reconcile flips a running row to stale when tmux session gone" {
    "$CODA_CORE_BIN" orchestrator new zeta --config-dir /tmp/zeta
    "$CODA_CORE_BIN" orchestrator start zeta --tmux-session coda-orch--zeta-ghost --pid 999999
    backdate_orch zeta

    run "$CODA_CORE_BIN" orchestrator reconcile
    [ "$status" -eq 0 ]
    [[ "$output" == *"stale"* ]]

    run "$CODA_CORE_BIN" orchestrator ls
    [[ "$output" == *"stale"* ]]
}

@test "v2: CODA_NO_AUTO_RECONCILE=1 disables lazy reconcile on ls" {
    "$CODA_CORE_BIN" orchestrator new lazy --config-dir /tmp/lazy
    "$CODA_CORE_BIN" orchestrator start lazy --tmux-session coda-orch--lazy-ghost --pid 999998
    backdate_orch lazy

    CODA_NO_AUTO_RECONCILE=1 run "$CODA_CORE_BIN" orchestrator ls
    [ "$status" -eq 0 ]
    [[ "$output" == *"running"* ]]
    [[ "$output" != *"stale"* ]]

    run "$CODA_CORE_BIN" orchestrator ls
    [ "$status" -eq 0 ]
    [[ "$output" == *"stale"* ]]
}

@test "v2: start from stale succeeds and clears stale_reason" {
    "$CODA_CORE_BIN" orchestrator new restart --config-dir /tmp/restart
    "$CODA_CORE_BIN" orchestrator start restart --tmux-session coda-orch--restart-ghost --pid 999997
    backdate_orch restart
    "$CODA_CORE_BIN" orchestrator reconcile >/dev/null

    run "$CODA_CORE_BIN" orchestrator start restart --tmux-session coda-orch--restart --pid 1234
    [ "$status" -eq 0 ]
    [[ "$output" == *"running"* ]]

    run "$CODA_CORE_BIN" orchestrator ls --json
    [ "$status" -eq 0 ]
    [[ "$output" != *"stale_reason"* ]]
}

@test "v2 routing: bash coda feature spawn routes to coda-core" {
    "$CODA_CORE_BIN" orchestrator new spawner --config-dir /tmp/x

    run env -i HOME="$HOME" PATH="$TEST_BIN_DIR:/usr/bin:/bin" CODA_HOME="$CODA_HOME" \
        CODA_PLUGINS_DIR="$TEST_BIN_DIR/empty-plugins" \
        bash -c '
        export AUTO_ATTACH_TMUX=false SSH_CONNECTION="" CODA_SKIP_ENV=true
        source "'"$SCRIPT_DIR"'/shell-functions.sh"
        coda feature spawn --orch spawner --project p --branch b --worktree /tmp/wt
    '
    [ "$status" -eq 0 ]
    [[ "$output" == *"spawned"* ]]
}
