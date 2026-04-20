#!/usr/bin/env bash
#
# test.sh — Smoke test for the coda MCP server
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER="$SCRIPT_DIR/server.js"
PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  [pass] $*"; }
fail() { FAIL=$((FAIL + 1)); echo "  [FAIL] $*"; }

if [ ! -d "$SCRIPT_DIR/node_modules" ]; then
    echo "node_modules missing. Run: npm install" >&2
    exit 1
fi

mcp_call() {
    local init='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0.0"}}}'
    local notify='{"jsonrpc":"2.0","method":"notifications/initialized"}'
    printf '%s\n%s\n%s\n' "$init" "$notify" "$1" | timeout 10 node "$SERVER" --stdio 2>/dev/null
}

mcp_call_env() {
    local env_args="$1"
    local msg="$2"
    local init='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0.0"}}}'
    local notify='{"jsonrpc":"2.0","method":"notifications/initialized"}'
    printf '%s\n%s\n%s\n' "$init" "$notify" "$msg" | timeout 10 env $env_args node "$SERVER" --stdio 2>/dev/null
}

echo "MCP Server Tests"
echo "================="
echo ""

# --- Test 1: Server initializes ---
echo "1. Server initialization"
response=$(printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0.0"}}}\n' | timeout 5 node "$SERVER" --stdio 2>/dev/null || true)
if echo "$response" | grep -q '"name":"coda"'; then
    pass "Server starts and identifies as 'coda'"
else
    fail "Server did not return expected identity"
fi

# --- Test 2: All core tools registered ---
echo "2. Core tool registration"
tools_response=$(mcp_call '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' | tail -1)

CORE_TOOLS=(
    coda_ls
    coda_project_ls
    coda_feature_ls
    coda_feature_start
    coda_feature_done
    coda_feature_finish
    coda_project_clone
    coda_project_create
    coda_project_workon
    coda_project_close
    coda_layout_ls
    coda_layout_show
    coda_help
)

missing=0
for tool in "${CORE_TOOLS[@]}"; do
    if ! echo "$tools_response" | grep -q "\"$tool\""; then
        fail "Missing core tool: $tool"
        missing=$((missing + 1))
    fi
done
if [ "$missing" -eq 0 ]; then
    pass "All ${#CORE_TOOLS[@]} core tools registered"
fi

# --- Test 3: Plugin tools NOT in core list ---
echo "3. Plugin tools absent from core"
PLUGIN_TOOLS=(coda_watch_status coda_watch_start coda_watch_stop coda_provider_status)
leaked=0
for tool in "${PLUGIN_TOOLS[@]}"; do
    if echo "$tools_response" | grep -q "\"$tool\""; then
        fail "Plugin tool leaked into core: $tool"
        leaked=$((leaked + 1))
    fi
done
if [ "$leaked" -eq 0 ]; then
    pass "No plugin tools in core-only list"
fi

# --- Test 4: Live tool call (coda_ls) ---
echo "4. Live tool call"
ls_response=$(mcp_call '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"coda_ls","arguments":{}}}' | tail -1)
if echo "$ls_response" | grep -q '"type":"text"'; then
    pass "coda_ls returned text content"
else
    fail "coda_ls did not return expected format"
fi

# --- Test 5: Bad args handled gracefully ---
echo "5. Error handling"
err_response=$(mcp_call '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"coda_layout_show","arguments":{}}}' | tail -1)
if echo "$err_response" | grep -qi 'error\|exit code\|invalid\|required'; then
    pass "Bad args handled gracefully"
else
    fail "Bad args call did not return an error response"
fi

# --- Test 6: Dynamic plugin tool registration ---
echo "6. Dynamic plugin tools"
TMPDIR_PLUGIN=$(mktemp -d)
trap 'rm -rf "$TMPDIR_PLUGIN"' EXIT

mkdir -p "$TMPDIR_PLUGIN/plugins/mock-plugin"
cat > "$TMPDIR_PLUGIN/plugins/mock-plugin/plugin.json" << 'PLUGIN_EOF'
{
  "name": "mock-plugin",
  "provides": {
    "mcp_tools": {
      "coda_mock_test": {
        "description": "A mock test tool",
        "command": ["help"]
      }
    }
  }
}
PLUGIN_EOF

cat > "$TMPDIR_PLUGIN/config.json" << 'CONFIG_EOF'
{"plugins": {"git@github.com:test/mock-plugin.git": {"enabled": true}}}
CONFIG_EOF

plugin_tools_response=$(mcp_call_env \
    "CODA_CONFIG_PATH=$TMPDIR_PLUGIN/config.json CODA_PLUGINS_DIR=$TMPDIR_PLUGIN/plugins" \
    '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' | tail -1)

if echo "$plugin_tools_response" | grep -q '"coda_mock_test"'; then
    pass "Dynamic plugin tool coda_mock_test registered"
else
    fail "Dynamic plugin tool coda_mock_test not found"
fi

# Verify core tools still present alongside plugin tools
if echo "$plugin_tools_response" | grep -q '"coda_ls"'; then
    pass "Core tools still present with plugin tools"
else
    fail "Core tools missing when plugin tools loaded"
fi

# --- Test 7: HTTP mode session handling ---
echo "7. HTTP mode session handling"
HTTP_PORT=3199
HTTP_URL="http://127.0.0.1:$HTTP_PORT/mcp"
HTTP_LOG=$(mktemp)
HTTP_PID=""
cleanup_http() {
    if [ -n "$HTTP_PID" ]; then
        kill "$HTTP_PID" 2>/dev/null || true
        wait "$HTTP_PID" 2>/dev/null || true
        HTTP_PID=""
    fi
    rm -f "$HTTP_LOG"
}
trap 'rm -rf "$TMPDIR_PLUGIN"; cleanup_http' EXIT

CODA_MCP_PORT=$HTTP_PORT node "$SERVER" >"$HTTP_LOG" 2>&1 &
HTTP_PID=$!

# Wait for /health to respond (up to ~5s)
for _ in $(seq 1 50); do
    if curl -sf "http://127.0.0.1:$HTTP_PORT/health" >/dev/null 2>&1; then
        break
    fi
    sleep 0.1
done

if ! curl -sf "http://127.0.0.1:$HTTP_PORT/health" >/dev/null 2>&1; then
    fail "HTTP server did not start on port $HTTP_PORT"
    echo "--- server log ---"
    cat "$HTTP_LOG"
else
    pass "HTTP server /health reachable"

    # 7a: No session id + initialize request -> 200 with Mcp-Session-Id header
    init_body='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0.0"}}}'
    init_headers=$(mktemp)
    curl -s -D "$init_headers" \
        -H 'Content-Type: application/json' \
        -H 'Accept: application/json, text/event-stream' \
        -X POST --data "$init_body" "$HTTP_URL" -o /dev/null
    init_status=$(awk 'NR==1{print $2}' "$init_headers")
    session_id=$(awk -F': ' 'tolower($1)=="mcp-session-id"{gsub(/\r/,"",$2); print $2}' "$init_headers")
    rm -f "$init_headers"

    if [ "$init_status" = "200" ] && [ -n "$session_id" ]; then
        pass "Initialize returns 200 with Mcp-Session-Id header"
    else
        fail "Initialize expected 200 + session id, got status=$init_status id='$session_id'"
    fi

    # 7b: Valid session id + tool call -> 200
    if [ -n "$session_id" ]; then
        call_body='{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
        call_status=$(curl -s -o /dev/null -w '%{http_code}' \
            -H 'Content-Type: application/json' \
            -H 'Accept: application/json, text/event-stream' \
            -H "Mcp-Session-Id: $session_id" \
            -X POST --data "$call_body" "$HTTP_URL")
        if [ "$call_status" = "200" ]; then
            pass "Valid session tool call returns 200"
        else
            fail "Valid session tool call expected 200, got $call_status"
        fi
    fi

    # 7c: Unknown session id + tool call -> 404 with "Session not found"
    stale_body_req='{"jsonrpc":"2.0","id":99,"method":"tools/list","params":{}}'
    stale_resp=$(mktemp)
    stale_status=$(curl -s -o "$stale_resp" -w '%{http_code}' \
        -H 'Content-Type: application/json' \
        -H 'Accept: application/json, text/event-stream' \
        -H 'Mcp-Session-Id: bogus-nonexistent-session' \
        -X POST --data "$stale_body_req" "$HTTP_URL")
    if [ "$stale_status" = "404" ]; then
        pass "Unknown session id returns HTTP 404"
    else
        fail "Unknown session id expected 404, got $stale_status"
    fi
    if grep -q 'Session not found' "$stale_resp"; then
        pass "Stale session body contains 'Session not found'"
    else
        fail "Stale session body missing 'Session not found': $(cat "$stale_resp")"
    fi
    if grep -q '"jsonrpc"' "$stale_resp"; then
        pass "Stale session body is JSON-RPC formatted"
    else
        fail "Stale session body not JSON-RPC formatted: $(cat "$stale_resp")"
    fi
    rm -f "$stale_resp"

    # 7d: No session id + non-initialize body -> 400
    bad_body='{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}'
    bad_resp=$(mktemp)
    bad_status=$(curl -s -o "$bad_resp" -w '%{http_code}' \
        -H 'Content-Type: application/json' \
        -H 'Accept: application/json, text/event-stream' \
        -X POST --data "$bad_body" "$HTTP_URL")
    if [ "$bad_status" = "400" ]; then
        pass "No session + non-initialize returns 400"
    else
        fail "No session + non-initialize expected 400, got $bad_status body=$(cat "$bad_resp")"
    fi
    rm -f "$bad_resp"
fi

# --- Test 8: HTTP mode version endpoint ---
echo "8. HTTP mode version endpoint"
if curl -sf "http://127.0.0.1:$HTTP_PORT/health" >/dev/null 2>&1; then
    version_resp=$(mktemp)
    version_status=$(curl -s -o "$version_resp" -w '%{http_code}' \
        "http://127.0.0.1:$HTTP_PORT/version")
    if [ "$version_status" = "200" ]; then
        pass "/version returns HTTP 200"
    else
        fail "/version expected 200, got $version_status"
    fi
    version_body=$(cat "$version_resp")
    if echo "$version_body" | grep -q '"sha"' && echo "$version_body" | grep -q '"started"'; then
        pass "/version body has sha and started fields"
    else
        fail "/version body missing sha/started: $version_body"
    fi
    reported_sha=$(echo "$version_body" | sed -n 's/.*"sha":"\([^"]*\)".*/\1/p')
    reported_started=$(echo "$version_body" | sed -n 's/.*"started":"\([^"]*\)".*/\1/p')
    if [ -n "$reported_sha" ] && [ -n "$reported_started" ]; then
        pass "/version sha and started are non-empty"
    else
        fail "/version sha='$reported_sha' started='$reported_started' (expected both non-empty)"
    fi
    expected_sha=$(git rev-parse --short HEAD 2>/dev/null)
    if [ -n "$expected_sha" ] && [ "$reported_sha" = "$expected_sha" ]; then
        pass "/version sha matches git rev-parse --short HEAD"
    elif [ -z "$expected_sha" ]; then
        pass "/version sha check skipped (not in a git repo)"
    else
        fail "/version sha '$reported_sha' != git HEAD '$expected_sha'"
    fi
    rm -f "$version_resp"
else
    fail "HTTP server not reachable for /version test"
fi

cleanup_http

# --- Summary ---
echo ""
echo "Results: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
