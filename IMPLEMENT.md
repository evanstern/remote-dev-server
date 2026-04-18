The brief is complete. Do not explore. Build what is described.

# Fix: Plugin dependency warning leaking into all coda command output

Focus card: #61

## Problem

Every `coda` command emits this on stderr:
```
warning: plugin 'orchestrator' requires 'opencode' which is not installed, skipping
```

opencode IS installed at `~/.opencode/bin/opencode` but `command -v` doesn't
find it because that path isn't always on `$PATH`.

## Changes

### 1. New `_coda_plugin_find_dep` helper (`lib/plugin.sh`)

Checks `command -v` first, then falls back to well-known install locations:
- `~/.opencode/bin/`
- `~/.local/bin/`

### 2. Demote plugin warnings to debug level (`lib/plugin.sh`)

Both the coda version constraint warning and the system dependency warning
now only emit when `CODA_DEBUG` is set. They were noisy and leaked into
every command's output.

### 3. Consistent dep checking in `_coda_plugin_install_deps`

Uses the same `_coda_plugin_find_dep` helper for consistency during
`coda plugin install`.

## Contract items

- [x] Unrelated plugin warnings do not appear in focus command output
- [x] opencode detection checks ~/.opencode/bin/ in addition to PATH

## Tests

New test file: `tests/plugin-dep-detection.sh`
- Finds deps on PATH
- Rejects nonexistent deps
- Finds deps in ~/.opencode/bin/
- Finds deps in ~/.local/bin/
- No warning output without CODA_DEBUG
- Debug output with CODA_DEBUG=1
