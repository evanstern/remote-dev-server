#!/usr/bin/env bash
#
# three-pane.sh — tmux layout: opencode (top-left), nvim (top-right), shell (bottom)
#
# Called by _coda_attach / coda layout apply with:
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
#
# Note: tmux 3.4 split-window -p (percentage) is broken — use -l (lines/cols).

_layout_init() {
    local session="$1" dir="$2" nvim_appname="${3:-nvim}"
    local cols="${COLUMNS:-200}" rows="${LINES:-50}"

    tmux new-session -d -s "$session" -x "$cols" -y "$rows" -c "$dir" "opencode; exec \$SHELL"

    local avail=$(( rows - 1 ))
    local bottom=$(( avail * 35 / 100 ))
    local half=$(( cols / 2 ))

    tmux split-window -v -t "$session:" -c "$dir" -l "$bottom"
    tmux select-pane -t "$session:" -U
    tmux split-window -h -t "$session:" -c "$dir" -l "$half" \
        "NVIM_APPNAME=$nvim_appname nvim .; exec \$SHELL"
    tmux select-pane -t "$session:" -L
}

_layout_spawn() {
    local session="$1" dir="$2" nvim_appname="${3:-nvim}"

    local script
    script=$(mktemp "${TMPDIR:-/tmp}/coda-layout.XXXXXX")
    cat > "$script" <<SETUP
#!/usr/bin/env bash
rm -f "\$0"
ph=\$(tmux display-message -p '#{pane_height}')
pw=\$(tmux display-message -p '#{pane_width}')
tmux split-window -v -c "$dir" -l \$(( ph * 35 / 100 ))
tmux select-pane -U
tmux split-window -h -c "$dir" -l \$(( pw / 2 )) \
    "NVIM_APPNAME=$nvim_appname nvim .; exec \\\$SHELL"
tmux select-pane -t "\$TMUX_PANE"
opencode; exec "\$SHELL"
SETUP
    chmod +x "$script"
    tmux new-window -t "$session" -c "$dir" "$script"
}

# Legacy alias
_layout_apply() { _layout_init "$@"; }
