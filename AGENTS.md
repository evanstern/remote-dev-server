# Coda Environment

You are running inside a **coda-managed session**: a tmux window with its own
git worktree on a dedicated feature branch. Other agents may be working on other
branches of the same repository in parallel — they cannot see your files and you
cannot see theirs.

## Finishing a feature

When the user says anything that signals the work is complete — for example:

- "commit this, merge, and we're done"
- "ship it"
- "we're done here"
- "wrap this up"
- "finish this feature"
- "that's everything, clean up"

…they are asking you to **run the full feature-finish sequence**:

### 1. Commit

Stage and commit all changes with a clear, conventional commit message.

```bash
git add -A
git commit -m "<type>: <concise summary>"
```

### 2. Push

Push the branch to the remote, setting the upstream if this is the first push.

```bash
git push -u origin "$(git branch --show-current)"
```

### 3. Create a pull request

Use the GitHub CLI. Write a real title and body — not placeholders.

```bash
gh pr create --fill-first
```

If `gh` is not available or the repo is not on GitHub, skip this step and tell
the user.

### 4. Clean up the session

Run the agent-safe cleanup command. This backgrounds the teardown (kills the
tmux session, removes the worktree, deletes the local branch) so it completes
even though you are inside the session being destroyed.

```bash
source ~/coda/shell-functions.sh && coda feature finish
```

**Always run step 4 last.** Once it executes, this session will shut down within
a few seconds.

## Important notes

- You are on branch `$(git branch --show-current)` inside a disposable worktree.
  Do not worry about keeping the worktree tidy after cleanup — it will be removed.
- `coda feature finish` detects the branch and project automatically from the
  working directory. It takes no arguments.
- `coda feature done <branch>` is the manual variant intended for humans running
  it from a *different* session. **Do not use `done` from within your own
  session** — it will kill the session synchronously before the worktree and
  branch cleanup can run.
- If any step fails, stop and report the error to the user. Do not proceed to
  the next step.
