#!/usr/bin/env bash
#
# default.sh — tmux layout: single pane running opencode
#
# Called by _coda_attach / coda layout apply with:
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

_layout_init() {
    local session="$1" dir="$2"
    tmux new-session -d -s "$session" -x "${COLUMNS:-200}" -y "${LINES:-50}" -c "$dir" "opencode; exec \$SHELL"
}

_layout_spawn() {
    local session="$1" dir="$2"
    tmux new-window -t "$session" -c "$dir" "opencode; exec \$SHELL"
}
