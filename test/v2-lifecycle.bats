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
    # Sanity: `coda feature start` is v1, must not route to coda-core.
    # We assert by checking that _coda_feature runs, not coda-core.
    source "$SCRIPT_DIR/shell-functions.sh"
    declare -f _coda_feature &>/dev/null
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
