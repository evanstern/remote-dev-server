#!/usr/bin/env bash
#
# three-pane.sh — tmux layout: opencode (top-left), nvim (top-right), shell (bottom)
#
# Called by _coda_attach() with:
#   $1 = session name
#   $2 = working directory
#   $3 = NVIM_APPNAME (optional, for config sandboxing)
#
# Layout:
#   ┌──────────────────┬──────────────────┐
#   │    opencode       │   nvim .          │  65% height
#   │                   │                   │
#   ├───────────────────┴──────────────────┤
#   │  $ shell                              │  35% height
#   └──────────────────────────────────────┘

_layout_apply() {
    local session="$1"
    local dir="$2"
    local nvim_appname="${3:-nvim}"

    tmux new-session -d -s "$session" -c "$dir" "opencode; exec \$SHELL"

    # -v = top/bottom split; bottom pane (shell) gets 35%
    tmux split-window -v -t "$session" -c "$dir" -p 35

    # -h = left/right split on top pane; right pane (nvim) gets 50%
    tmux split-window -h -t "${session}.0" -c "$dir" -p 50 \
        "NVIM_APPNAME=$nvim_appname nvim .; exec \$SHELL"

    tmux select-pane -t "${session}.0"
}
