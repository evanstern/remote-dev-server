#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT_DIR/tests/testlib.sh"

TEST_TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TEST_TMPDIR"' EXIT

export HOME="$TEST_TMPDIR/home"
export PROJECTS_DIR="$TEST_TMPDIR/projects"
export SESSION_PREFIX="coda-"
export DEFAULT_BRANCH="main"
export GIT_REMOTE="origin"
export DEFAULT_LAYOUT="default"
export CODA_LAYOUTS_DIR="$ROOT_DIR/layouts"
export CODA_PROFILES_DIR="$TEST_TMPDIR/profiles"
export AUTO_ATTACH_TMUX="false"
export CODA_TEST_STATE_DIR="$TEST_TMPDIR/state"
export PATH="$ROOT_DIR/tests/bin:$PATH"
unset TMUX

mkdir -p "$HOME" "$PROJECTS_DIR" "$CODA_PROFILES_DIR" "$CODA_TEST_STATE_DIR"

REMOTE_REPO="$TEST_TMPDIR/remote.git"
SEED_REPO="$TEST_TMPDIR/seed"

git init "$SEED_REPO" -b main >/dev/null
git -C "$SEED_REPO" config user.name 'Coda Test'
git -C "$SEED_REPO" config user.email 'coda-test@example.com'
printf 'hello\n' > "$SEED_REPO/README.md"
git -C "$SEED_REPO" add README.md >/dev/null
git -C "$SEED_REPO" commit -m 'Initial commit' >/dev/null
git clone --bare "$SEED_REPO" "$REMOTE_REPO" >/dev/null

export CODA_SKIP_ENV=true
source "$ROOT_DIR/shell-functions.sh"

printf 'Running core lifecycle tests...\n'

assert_eq "demo-app" "$(_coda_sanitize_session_name 'demo.app')" "session name should replace dots"

project_start_output="$(coda project start --repo "$REMOTE_REPO" demo.app 2>&1)"
assert_contains "$project_start_output" "Project ready: $PROJECTS_DIR/demo.app" "project start should clone into projects dir"
assert_contains "$project_start_output" "Opening session in $PROJECTS_DIR/demo.app/main" "project start should open main worktree"

assert_file_exists "$PROJECTS_DIR/demo.app/.bare" "project add should create bare repo"
assert_file_exists "$PROJECTS_DIR/demo.app/.git" "project add should write gitdir pointer"
assert_file_exists "$PROJECTS_DIR/demo.app/main/.git" "project add should create default branch worktree"

assert_contains "$(cat "$CODA_TEST_STATE_DIR/tmux-sessions")" "coda-demo-app" "project start should create base session"
assert_contains "$(cat "$CODA_TEST_STATE_DIR/tmux-actions")" "attach:coda-demo-app" "project start should attach to session"

list_output="$(coda ls 2>&1)"
assert_contains "$list_output" "Active sessions:" "coda ls should report sessions"
assert_contains "$list_output" "coda-demo-app" "coda ls should include project session"

cd "$PROJECTS_DIR/demo.app/main"
reconnect_output="$(coda project start 2>&1)"
assert_contains "$(tail -n 1 "$CODA_TEST_STATE_DIR/tmux-actions")" "attach:coda-demo-app" "reconnect should attach existing session"
assert_eq "" "$reconnect_output" "reconnect should succeed without error output"

feature_output="$(coda feature start feature-one 2>&1)"
assert_contains "$feature_output" "Creating worktree: feature-one (from main)" "feature start should create worktree from default branch"
assert_file_exists "$PROJECTS_DIR/demo.app/feature-one/.git" "feature start should create feature worktree"
assert_contains "$(cat "$CODA_TEST_STATE_DIR/tmux-sessions")" "coda-demo-app--feature-one" "feature start should create feature session"

switch_output="$(coda switch 2>&1)"
assert_eq "" "$switch_output" "coda switch should succeed silently with stub fzf"
assert_contains "$(tail -n 1 "$CODA_TEST_STATE_DIR/tmux-actions")" "attach:coda-demo-app" "switch should attach selected session"

done_output="$(coda feature done feature-one 2>&1)"
assert_contains "$done_output" "Cleaning up feature: feature-one" "feature done should announce cleanup"
assert_not_exists "$PROJECTS_DIR/demo.app/feature-one" "feature done should remove worktree"

if git -C "$PROJECTS_DIR/demo.app" show-ref --verify --quiet refs/heads/feature-one; then
    fail "feature done should delete the feature branch"
fi

if grep -Fx -- 'coda-demo-app--feature-one' "$CODA_TEST_STATE_DIR/tmux-sessions" >/dev/null 2>&1; then
    fail "feature done should kill the feature session"
fi

dev_project_output="$(coda-dev project start --repo "$REMOTE_REPO" rewrite.app 2>&1)"
assert_contains "$dev_project_output" "Project ready: $PROJECTS_DIR/rewrite.app" "coda-dev project start should clone into projects dir"
assert_contains "$(cat "$CODA_TEST_STATE_DIR/tmux-sessions")" "coda-dev-rewrite-app" "coda-dev should use its own session prefix"

dev_list_output="$(coda-dev ls 2>&1)"
assert_contains "$dev_list_output" "coda-dev-rewrite-app" "coda-dev ls should include dev sessions"
assert_not_contains "$dev_list_output" "coda-demo-app" "coda-dev ls should not show stable coda sessions"

cd "$PROJECTS_DIR/rewrite.app/main"
dev_feature_output="$(coda-dev feature start feature-two 2>&1)"
assert_contains "$dev_feature_output" "Creating worktree: feature-two (from main)" "coda-dev feature start should create worktree"
assert_contains "$(cat "$CODA_TEST_STATE_DIR/tmux-sessions")" "coda-dev-rewrite-app--feature-two" "coda-dev feature start should use dev session prefix"

dev_done_output="$(coda-dev feature done feature-two 2>&1)"
assert_contains "$dev_done_output" "Cleaning up feature: feature-two" "coda-dev feature done should announce cleanup"
assert_not_exists "$PROJECTS_DIR/rewrite.app/feature-two" "coda-dev feature done should remove worktree"

printf 'PASS: core lifecycle tests\n'
