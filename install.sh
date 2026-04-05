#!/usr/bin/env bash
#
# install.sh - Install remote dev server configs and shell functions
#
# Idempotent: safe to run multiple times. Skips unchanged configs, avoids
# duplicate entries, only backs up when content actually differs.
#
# Usage:
#   ./install.sh           (interactive, prompts before overwriting)
#   ./install.sh --force   (overwrite without prompting)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FORCE="${1:-}"

confirm() {
    if [ "$FORCE" = "--force" ]; then
        return 0
    fi
    local msg="$1"
    read -rp "$msg [y/N] " response
    [[ "$response" =~ ^[Yy]$ ]]
}

backup_if_exists() {
    local file="$1"
    if [ -f "$file" ]; then
        local backup="${file}.backup.$(date +%Y%m%d%H%M%S)"
        cp "$file" "$backup"
        echo "  Backed up existing: $backup"
    fi
}

echo "=== Remote Dev Server Install ==="
echo ""

if [ ! -f "$SCRIPT_DIR/.env" ]; then
    echo "No .env found. Creating from template..."
    cp "$SCRIPT_DIR/.env.example" "$SCRIPT_DIR/.env"
    echo "  Edit $SCRIPT_DIR/.env with your settings before continuing."
    echo ""
fi

# shellcheck source=/dev/null
source "$SCRIPT_DIR/.env"

echo "Installing with config:"
echo "  Projects dir: ${PROJECTS_DIR:-~/projects}"
echo "  Session prefix: ${SESSION_PREFIX:-oc-}"
echo "  Max sessions: ${MAX_CONCURRENT_SESSIONS:-5}"
echo ""

# --- tmux config ---

if [ -f "$HOME/.tmux.conf" ] && diff -q "$SCRIPT_DIR/tmux.conf" "$HOME/.tmux.conf" &>/dev/null; then
    echo "  ~/.tmux.conf is already up to date"
elif confirm "Install tmux.conf to ~/.tmux.conf?"; then
    backup_if_exists "$HOME/.tmux.conf"
    cp "$SCRIPT_DIR/tmux.conf" "$HOME/.tmux.conf"
    echo "  Installed ~/.tmux.conf"
fi

# --- OpenCode TUI config ---

mkdir -p "$HOME/.config/opencode"
if [ -f "$HOME/.config/opencode/tui.json" ] && diff -q "$SCRIPT_DIR/tui.json.example" "$HOME/.config/opencode/tui.json" &>/dev/null; then
    echo "  ~/.config/opencode/tui.json is already up to date"
elif confirm "Install vim-style OpenCode keybinds to ~/.config/opencode/tui.json?"; then
    backup_if_exists "$HOME/.config/opencode/tui.json"
    cp "$SCRIPT_DIR/tui.json.example" "$HOME/.config/opencode/tui.json"
    echo "  Installed ~/.config/opencode/tui.json"
fi

# --- Shell functions ---

SHELL_RC=""
if [ -f "$HOME/.zshrc" ]; then
    SHELL_RC="$HOME/.zshrc"
elif [ -f "$HOME/.bashrc" ]; then
    SHELL_RC="$HOME/.bashrc"
fi

if [ -n "$SHELL_RC" ]; then
    SOURCE_LINE="source $SCRIPT_DIR/shell-functions.sh"
    if grep -qF "$SOURCE_LINE" "$SHELL_RC" 2>/dev/null; then
        echo "  Shell functions already sourced in $SHELL_RC"
    elif confirm "Add shell functions to $SHELL_RC?"; then
        echo "" >> "$SHELL_RC"
        echo "# Remote dev server shell functions" >> "$SHELL_RC"
        echo "$SOURCE_LINE" >> "$SHELL_RC"
        echo "  Added source line to $SHELL_RC"
    fi
else
    echo "  No .zshrc or .bashrc found. Add this manually:"
    echo "    source $SCRIPT_DIR/shell-functions.sh"
fi

# --- SSH config for keepalive ---

if grep -q "^ClientAliveInterval" /etc/ssh/sshd_config 2>/dev/null; then
    echo "  SSH keepalive already configured, skipping"
elif confirm "Configure SSH keepalive? (updates /etc/ssh/sshd_config)"; then
    echo "" | sudo tee -a /etc/ssh/sshd_config >/dev/null
    echo "ClientAliveInterval 60" | sudo tee -a /etc/ssh/sshd_config >/dev/null
    echo "ClientAliveCountMax 3" | sudo tee -a /etc/ssh/sshd_config >/dev/null
    sudo systemctl restart sshd 2>/dev/null || sudo systemctl restart ssh 2>/dev/null || true
    echo "  SSH keepalive configured (60s interval, 3 max)"
fi

# --- Project directory ---

mkdir -p "${PROJECTS_DIR:-$HOME/projects}"

echo ""
echo "=== Install Complete ==="
echo ""
echo "Next steps:"
echo "  1. Review/edit: $SCRIPT_DIR/.env"
echo "  2. Start tmux:  tmux"
echo "  3. Install tmux plugins: prefix + I (inside tmux)"
echo "  4. Reload shell: source $SHELL_RC"
echo "  5. Sign in to Claude: claude auth login"
echo "  6. Enable Claude auth in OpenCode: oc-auth-setup"
echo "  7. Try it:       setup-project <git-repo-url>"
echo ""
