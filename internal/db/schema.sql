CREATE TABLE IF NOT EXISTS orchestrators (
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,
  config_dir    TEXT NOT NULL,
  state         TEXT NOT NULL
                CHECK (state IN ('stopped','starting','running','stopping')),
  tmux_session  TEXT,
  session_id    TEXT,
  port          INTEGER,
  pid           INTEGER,
  started_at    INTEGER,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS features (
  id              INTEGER PRIMARY KEY,
  name            TEXT NOT NULL,
  orchestrator_id INTEGER NOT NULL REFERENCES orchestrators(id) ON DELETE CASCADE,
  project         TEXT NOT NULL,
  branch          TEXT NOT NULL,
  worktree_dir    TEXT NOT NULL,
  tmux_session    TEXT,
  state           TEXT NOT NULL
                  CHECK (state IN ('spawning','running','reporting','done','failed')),
  brief_path      TEXT,
  pr_url          TEXT,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  ended_at        INTEGER,
  UNIQUE (orchestrator_id, name)
);

CREATE TABLE IF NOT EXISTS hook_events (
  id         INTEGER PRIMARY KEY,
  event      TEXT NOT NULL,
  plugin     TEXT NOT NULL,
  payload    TEXT NOT NULL,
  exit_code  INTEGER NOT NULL,
  stderr     TEXT,
  fired_at   INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_hook_events_fired_at ON hook_events(fired_at);

CREATE TABLE IF NOT EXISTS schema_version (
  version    INTEGER NOT NULL PRIMARY KEY,
  applied_at INTEGER NOT NULL
);

INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (1, unixepoch());
