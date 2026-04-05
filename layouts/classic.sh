#!/usr/bin/env bash
#
# classic.sh — tmux layout: single pane running opencode (original behavior)
#
# Called by _coda_attach() with:
#   $1 = session name
#   $2 = working directory
#   $3 = NVIM_APPNAME (unused in this layout)
#
# Layout:
#   ┌──────────────────────────────────────┐
#   │                                       │
#   │              opencode                 │
#   │          (falls back to $SHELL)       │
#   │                                       │
#   └──────────────────────────────────────┘

_layout_apply() {
    local session="$1"
    local dir="$2"

    tmux new-session -d -s "$session" -c "$dir" "opencode; exec \$SHELL"
}
