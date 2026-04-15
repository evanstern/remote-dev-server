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

_coda_mcp_start() {
    local mcp_session="$1"

    if tmux has-session -t "$mcp_session" 2>/dev/null; then
        echo "MCP server already running on port $CODA_MCP_PORT."
        echo "  View:    tmux attach -t $mcp_session"
        echo "  Stop:    coda mcp stop"
        echo "  Restart: coda mcp restart"
        return 0
    fi

    local server_js="$_CODA_DIR/mcp-server/server.js"
    if [ ! -f "$server_js" ]; then
        echo "MCP server not found: $server_js"
        return 1
    fi

    local mcp_cmd="CODA_MCP_PORT=$CODA_MCP_PORT CODA_DIR=$_CODA_DIR node $server_js"

    tmux new-session -d -s "$mcp_session" "$mcp_cmd"
    echo "MCP server started on port $CODA_MCP_PORT."
    echo "  View:    tmux attach -t $mcp_session"
    echo "  Stop:    coda mcp stop"
    echo "  Health:  curl http://127.0.0.1:$CODA_MCP_PORT/health"
}

_coda_mcp_stop() {
    local mcp_session="$1"

    if ! tmux has-session -t "$mcp_session" 2>/dev/null; then
        echo "MCP server is not running."
        return 0
    fi

    tmux kill-session -t "$mcp_session"
    echo "MCP server stopped."
}

_coda_mcp_status() {
    local mcp_session="$1"

    if tmux has-session -t "$mcp_session" 2>/dev/null; then
        local created
        created=$(tmux display-message -t "$mcp_session" -p '#{t:session_created}' 2>/dev/null)
        echo "MCP server: running (since $created)"
        echo "  Port:    $CODA_MCP_PORT"
        echo "  View:    tmux attach -t $mcp_session"
        echo "  Stop:    coda mcp stop"

        if command -v curl &>/dev/null; then
            local health
            health=$(curl -sf "http://127.0.0.1:$CODA_MCP_PORT/health" 2>/dev/null)
            if [ -n "$health" ]; then
                echo "  Health:  ok"
            else
                echo "  Health:  not responding"
            fi
        fi
    else
        echo "MCP server: stopped"
        echo "  Start: coda mcp"
    fi
}
