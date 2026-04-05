# Remote Dev Server: OpenCode + tmux on Proxmox

A headless development server running multiple OpenCode instances across multiple
projects, accessible from anywhere via Tailscale.

## Architecture

```
+-----------------------------------------------------+
|  YOUR DEVICES (laptop, phone, tablet)                |
|  Termius / Blink / Moshi --> mosh over Tailscale     |
+-------------------------+---------------------------+
                          |  Tailscale mesh (100.x.x.x)
+-------------------------v---------------------------+
|  PROXMOX VM  (Ubuntu Server 24.04, KVM)              |
|                                                      |
|  tmux server                                         |
|  |-- session: oc-project-a        (main branch)      |
|  |   \-- opencode                                    |
|  |-- session: oc-project-a--auth  (worktree)         |
|  |   \-- opencode                                    |
|  |-- session: oc-project-b        (main branch)      |
|  |   \-- opencode                                    |
|  |-- session: oc-project-b--api   (worktree)         |
|  |   \-- opencode                                    |
|  \-- session: oc-project-c        (main branch)      |
|      \-- opencode                                    |
|                                                      |
|  ~/projects/                                         |
|  |-- project-a/                                      |
|  |   |-- .bare/          (bare git repo)             |
|  |   |-- main/           (worktree)                  |
|  |   \-- auth/           (worktree -> feat/auth)     |
|  |-- project-b/                                      |
|  |   |-- .bare/                                      |
|  |   |-- main/                                       |
|  |   \-- api/            (worktree -> feat/api)      |
|  \-- project-c/                                      |
|      |-- .bare/                                      |
|      \-- main/                                       |
+------------------------------------------------------+
```

## Design Decisions

### Why KVM over LXC

OpenCode executes shell commands, npm scripts, and arbitrary code on your behalf.
LXC containers share the host kernel. If an agent runs something unexpected, it
has a direct path to your Proxmox host. KVM virtualizes at the hypervisor level,
so a misbehaving agent is trapped inside the VM.

Use LXC for inference APIs (Ollama, LocalAI). Use KVM for anything that runs
untrusted code.

### Why ext4 over ZFS/Btrfs

Git worktrees generate heavy small-file metadata operations. Copy-on-write
filesystems (ZFS, Btrfs) add latency for those operations with no real benefit
here. Your snapshots are git branches. ext4 is fast, stable, and has zero
overhead for this workload.

### Why mosh over plain SSH

mosh uses UDP instead of TCP. It survives WiFi drops, cellular handoffs, laptop
sleep, and high-latency connections. It does local echo (your keystrokes appear
instantly even on slow links) and reconnects transparently. SSH over unreliable
connections hangs and drops sessions. mosh does not.

The resilience stack is three layers deep:
- **Tailscale**: stable IP from any network, NAT traversal, no port forwarding
- **mosh**: survives connection interruptions
- **tmux**: sessions persist regardless of connection state

### Why bare repo + worktrees

The bare repository pattern keeps everything for a project under one directory.
Instead of a regular clone with worktrees scattered as siblings, you clone bare
and create all checkouts as worktrees inside the project folder.

```
~/projects/myapp/
|-- .bare/             # all git data
|-- .git               # pointer file to .bare
|-- main/              # worktree: main branch
|-- auth/              # worktree: feat/auth
\-- streaming/         # worktree: feat/streaming
```

Benefits:
- `ls` shows your active work at a glance
- Delete the project folder and everything is gone (no orphans)
- Each OpenCode agent gets an isolated, clean checkout
- No merge conflicts between parallel agents

### Why OpenCode over Claude Code for this setup

OpenCode has a proper headless story that Claude Code lacks:

| Capability             | OpenCode              | Claude Code          |
|------------------------|-----------------------|----------------------|
| Non-interactive CLI    | `opencode run`        | `claude run`         |
| Headless HTTP server   | `opencode serve`      | No                   |
| Remote attachment      | `opencode attach`     | No                   |
| JSON output            | `--format json`       | No                   |
| Async task submission  | `/prompt_async` API   | No                   |
| Programmatic API       | Full OpenAPI 3.1 spec | No                   |

The HTTP server + async API means you can fire tasks from scripts, cron jobs,
or even your phone, and check results later.

## VM Sizing

For up to 5 concurrent OpenCode instances:

| Resource | Allocation | Rationale                                     |
|----------|------------|-----------------------------------------------|
| CPU      | 8 vCPUs    | ~2 per active instance + headroom for git/npm  |
| RAM      | 24-32 GB   | ~2-4 GB per instance + OS + buffer             |
| Disk     | 100-150 GB | OS (20GB) + repos/worktrees (80-130GB)         |
| OS       | Ubuntu 24.04 LTS | Best Node.js support, 5-year LTS        |

OpenCode instances are mostly idle (waiting on API responses) with brief spikes
during tool execution. They are API callers, not compute workloads. The main
resource concern is memory, since Node.js processes can grow over long sessions.

Consider restarting long-running instances every 4-6 hours to reclaim memory.

## Layers

### Layer 1: VM Setup

See: `setup-vm.sh`

Installs all required packages: git, tmux, mosh, Node.js, fzf, and their
dependencies. Run this once on a fresh Ubuntu Server 24.04 VM.

### Layer 2: Tailscale + Remote Access

Install Tailscale inside the VM (not on the Proxmox host):

```bash
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up
```

Gotcha: do NOT install Tailscale on the Proxmox host with `--advertise-routes`
for your VM subnet. It can cause the host to route its own local traffic through
the VPN tunnel, breaking cluster communication.

For mobile access:
- **Laptop**: mosh + tmux via any terminal
- **Phone/tablet**: Termius or Blink Shell over Tailscale
- **Notification when agents need input**: consider ntfy (self-hosted push
  notifications) or similar webhook service

### Layer 3: tmux Session Management

See: `tmux.conf`

The tmux config includes:
- **tmux-resurrect + tmux-continuum** for session persistence across reboots
- **fzf session switcher** (bound to prefix + f)
- **Auto-attach on SSH** so you land in tmux immediately

### Layer 4: Shell Functions

See: `shell-functions.sh`

Core functions:

| Function          | Purpose                                          |
|-------------------|--------------------------------------------------|
| `oc <name> [dir]` | Create or attach to an OpenCode tmux session     |
| `ocs`             | List all active OpenCode sessions                |
| `tm`              | fzf-powered session switcher with pane preview   |
| `setup-project`   | Clone a repo as bare + create main worktree      |
| `feature <name>`  | Create a worktree + tmux session for a feature   |
| `done-feature`    | Kill session, remove worktree, delete branch     |
| `list-features`   | Show all worktrees for the current project       |
| `oc-serve [port]` | Start OpenCode in headless server mode           |
| `oc-auth-setup`   | Configure OpenCode to use Claude Code auth       |

Source these in your `.bashrc` or `.zshrc`:

```bash
source ~/remote-dev-server/shell-functions.sh
```

### Layer 4.5: Claude Max Auth on Linux

On macOS, community auth plugins can read Claude Code tokens from Keychain. On a
Linux VM, the working path is simpler: Claude Code stores OAuth credentials in
`~/.claude/.credentials.json`, and OpenCode can reuse them through the
`opencode-claude-auth` plugin.

One-time setup on the VM:

```bash
claude auth login
claude auth status

# after sourcing shell-functions.sh
oc-auth-setup
```

What `oc-auth-setup` does:
- Verifies `claude` and `opencode` are installed
- Verifies Claude Code is logged in
- Verifies `~/.claude/.credentials.json` exists
- Installs `opencode-claude-auth` globally via `opencode plugin ... -g`

Verification:

```bash
opencode models anthropic
opencode run --model anthropic/claude-sonnet-4-5 "Reply with exactly: auth-ok"
```

If Claude auth expires later, refresh it with `claude auth login` and rerun
`oc-auth-setup`.

### Layer 5: OpenCode Automation

For interactive use, the shell functions handle everything. For fire-and-forget:

```bash
# Start a headless OpenCode server (auto-approve all actions)
OPENCODE_PERMISSION='{"*":"allow"}' opencode serve --port 4096 &

# Submit a task (returns immediately)
curl -X POST http://localhost:4096/session/$SESSION_ID/prompt_async \
    -d '{"parts":[{"type":"text","text":"Add input validation to auth handler"}]}'

# Attach a TUI to monitor
opencode attach http://localhost:4096

# Or run a one-shot task with JSON output
opencode run --format json "Refactor error handling in src/api/" > task.jsonl &
```

The permission system is granular. For autonomous background work, allow
everything. For sessions you monitor interactively, use the default ask mode:

```bash
# Permissive (fire-and-forget)
OPENCODE_PERMISSION='{"*":"allow"}' opencode serve --port 4096

# Restrictive (interactive, asks before dangerous operations)
opencode  # default behavior
```

### Layer 6: OpenCode TUI Keybinds

See: `tui.json.example`

The bundled TUI config keeps OpenCode on its default leader key but adds a
small set of vim-style navigation bindings for cursor movement and session
navigation.

## Daily Workflow

```
Morning (laptop):
  ssh into VM -> land in tmux automatically
  ocs                             # see what's running
  tm                              # fzf-pick a session to check on
  # review what agents did overnight

Working session:
  feature auth                    # new worktree + session
  feature api-v2                  # another feature in parallel
  # switch between them with tm

Away from desk (phone):
  mosh into VM via Tailscale
  ocs                             # quick status check
  tmux send-keys -t oc-auth "add tests for the login flow" Enter
  # pocket phone, agent works autonomously

Next morning:
  tm -> oc-auth                   # see what it did
  done-feature auth               # merge, cleanup worktree + session
```

## File Structure

```
remote-dev-server/
|-- .env.example          # Config template (projects dir, ports, etc.)
|-- setup-vm.sh           # One-time VM bootstrap
|-- tui.json.example      # OpenCode TUI keybind template
|-- tmux.conf             # Reference tmux configuration
|-- shell-functions.sh    # Sourceable shell functions
\-- install.sh            # Installer that ties everything together
```

## Tweaking

Everything is parameterized via `.env.example`.
Copy it to `.env`, fill in your values, and the shell functions read from it.

Key knobs:
- `PROJECTS_DIR`: where repos live (default: `~/projects`)
- `OPENCODE_BASE_PORT`: starting port for OpenCode serve instances
- `MAX_CONCURRENT_SESSIONS`: cap on parallel sessions (default: 5)
- `SESSION_PREFIX`: tmux session name prefix (default: `oc-`)
- `DEFAULT_BRANCH`: default branch name for new worktrees (default: `main`)
