#!/usr/bin/env bash
#
# mcp.sh — coda mcp-server management (start/stop/status/restart)
#

CODA_MCP_PORT="${CODA_MCP_PORT:-3111}"

_coda_mcp() {
    local subcmd="${1:-start}"
    local mcp_session="${SESSION_PREFIX}mcp-server"

    case "$subcmd" in
        start)   _coda_mcp_start "$mcp_session" ;;
        stop)    _coda_mcp_stop "$mcp_session" ;;
        status)  _coda_mcp_status "$mcp_session" ;;
        restart) _coda_mcp_stop "$mcp_session" && _coda_mcp_start "$mcp_session" ;;
        ""|help) echo "Usage: coda mcp <start|stop|status|restart>" ;;
        *)       echo "Unknown mcp subcommand: $subcmd"; echo "Usage: coda mcp <start|stop|status|restart>"; return 1 ;;
    esac
}

_coda_mcp_port_pid() {
    local port="${1:-$CODA_MCP_PORT}"
    # ss output with -p includes 'users:(("proc",pid=1234,fd=5))'.
    # -H strips the header. -t = TCP, -l = listening, -n = numeric.
    # Match listeners bound to :port EXACTLY (anchor with `sport = :port`)
    # so :31111 doesn't match :3111.
    ss -Hltnp "sport = :$port" 2>/dev/null \
        | awk 'match($0, /pid=[0-9]+/) {
                 s = substr($0, RSTART+4, RLENGTH-4);
                 print s;
                 exit
               }'
}

_coda_mcp_tmux_running() {
    local mcp_session="$1"
    tmux has-session -t "$mcp_session" 2>/dev/null
}

_coda_mcp_start() {
    local mcp_session="$1"
    local port="$CODA_MCP_PORT"
    local tmux_running=false
    _coda_mcp_tmux_running "$mcp_session" && tmux_running=true

    local port_pid
    port_pid=$(_coda_mcp_port_pid "$port")

    if $tmux_running && [ -n "$port_pid" ]; then
        echo "MCP server already running on port $port (pid $port_pid)."
        echo "  View:    tmux attach -t $mcp_session"
        echo "  Stop:    coda mcp stop"
        echo "  Restart: coda mcp restart"
        return 0
    fi

    if $tmux_running && [ -z "$port_pid" ]; then
        echo "MCP server tmux session exists but nothing is listening on port $port."
        echo "  View:    tmux attach -t $mcp_session"
        echo "  Fix:     coda mcp restart"
        return 1
    fi

    if ! $tmux_running && [ -n "$port_pid" ]; then
        echo "Port $port is already bound by pid $port_pid (not managed by coda)."
        echo "  Inspect: ps -p $port_pid"
        echo "  Adopt:   coda mcp restart    (kills pid $port_pid, then starts)"
        echo "  Free:    kill $port_pid      (if you want to start fresh manually)"
        return 1
    fi

    # Neither tmux nor port in use -- clean start.
    local server_js="$_CODA_DIR/mcp-server/server.js"
    if [ ! -f "$server_js" ]; then
        echo "MCP server not found: $server_js"
        return 1
    fi

    local mcp_cmd="CODA_MCP_PORT=$port CODA_DIR=$_CODA_DIR node $server_js"

    tmux new-session -d -s "$mcp_session" "$mcp_cmd"
    echo "MCP server started on port $port."
    echo "  View:    tmux attach -t $mcp_session"
    echo "  Stop:    coda mcp stop"
    echo "  Health:  curl http://127.0.0.1:$port/health"
}

_coda_mcp_stop() {
    local mcp_session="$1"
    local port="$CODA_MCP_PORT"
    local tmux_running=false
    _coda_mcp_tmux_running "$mcp_session" && tmux_running=true

    local killed_tmux=false
    if $tmux_running; then
        tmux kill-session -t "$mcp_session"
        killed_tmux=true
    fi

    # tmux kill doesn't always stop the child immediately; give it a beat.
    sleep 0.2

    local port_pid
    port_pid=$(_coda_mcp_port_pid "$port")

    if [ -n "$port_pid" ]; then
        echo "Warning: port $port still bound after tmux cleanup (pid $port_pid)."
        echo "  Killing rogue process..."
        kill "$port_pid" 2>/dev/null
        # Give it 1s; escalate to SIGKILL if needed.
        sleep 1
        port_pid=$(_coda_mcp_port_pid "$port")
        if [ -n "$port_pid" ]; then
            echo "  Process did not exit after SIGTERM; sending SIGKILL."
            kill -9 "$port_pid" 2>/dev/null
        fi
        echo "MCP server stopped (including rogue process)."
        return 0
    fi

    if $killed_tmux; then
        echo "MCP server stopped."
    else
        echo "MCP server was not running."
    fi
}

_coda_mcp_status() {
    local mcp_session="$1"
    local port="$CODA_MCP_PORT"
    local tmux_running=false
    _coda_mcp_tmux_running "$mcp_session" && tmux_running=true

    local port_pid
    port_pid=$(_coda_mcp_port_pid "$port")

    if $tmux_running && [ -n "$port_pid" ]; then
        local created
        created=$(tmux display-message -t "$mcp_session" -p '#{t:session_created}' 2>/dev/null)
        echo "MCP server: running (since $created)"
        echo "  Port:    $port (pid $port_pid)"
        echo "  View:    tmux attach -t $mcp_session"
        echo "  Stop:    coda mcp stop"
        if command -v curl &>/dev/null; then
            local health
            health=$(curl -sf "http://127.0.0.1:$port/health" 2>/dev/null)
            if [ -n "$health" ]; then
                echo "  Health:  ok"
            else
                echo "  Health:  not responding"
            fi
        fi
        return 0
    fi

    if $tmux_running && [ -z "$port_pid" ]; then
        echo "MCP server: tmux session exists but port $port not bound (broken state)."
        echo "  Fix: coda mcp restart"
        return 1
    fi

    if ! $tmux_running && [ -n "$port_pid" ]; then
        echo "MCP server: rogue (port $port bound by pid $port_pid, no tmux session)."
        echo "  Inspect: ps -p $port_pid"
        echo "  Adopt:   coda mcp restart"
        return 1
    fi

    echo "MCP server: stopped"
    echo "  Start: coda mcp"
}
