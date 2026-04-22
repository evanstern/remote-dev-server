-- Extend orchestrators.state CHECK to include 'stale' and add
-- stale_reason column. SQLite does not support modifying CHECK
-- constraints in place, so use the table-swap pattern.
CREATE TABLE orchestrators_new (
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,
  config_dir    TEXT NOT NULL,
  state         TEXT NOT NULL
                CHECK (state IN ('stopped','starting','running','stopping','stale')),
  tmux_session  TEXT,
  session_id    TEXT,
  port          INTEGER,
  pid           INTEGER,
  started_at    INTEGER,
  stale_reason  TEXT,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

INSERT INTO orchestrators_new
  (id, name, config_dir, state, tmux_session, session_id, port, pid,
   started_at, stale_reason, created_at, updated_at)
  SELECT id, name, config_dir, state, tmux_session, session_id, port, pid,
         started_at, NULL, created_at, updated_at
  FROM orchestrators;

DROP TABLE orchestrators;
ALTER TABLE orchestrators_new RENAME TO orchestrators;

-- Extend features.state CHECK to include 'stale' alongside 'failed'.
-- 'stale' = reconciler verdict from a dead liveness probe.
-- 'failed' = feature errored on its own terms (reserved for future use).
-- Same table-swap pattern; also adds the stale_reason column here so
-- the whole migration stays rebuild-based.
CREATE TABLE features_new (
  id              INTEGER PRIMARY KEY,
  name            TEXT NOT NULL,
  orchestrator_id INTEGER NOT NULL REFERENCES orchestrators(id) ON DELETE CASCADE,
  project         TEXT NOT NULL,
  branch          TEXT NOT NULL,
  worktree_dir    TEXT NOT NULL,
  tmux_session    TEXT,
  state           TEXT NOT NULL
                  CHECK (state IN ('spawning','running','reporting','done','failed','stale')),
  brief_path      TEXT,
  pr_url          TEXT,
  stale_reason    TEXT,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  ended_at        INTEGER,
  UNIQUE (orchestrator_id, name)
);

INSERT INTO features_new
  (id, name, orchestrator_id, project, branch, worktree_dir, tmux_session,
   state, brief_path, pr_url, stale_reason, created_at, updated_at, ended_at)
  SELECT id, name, orchestrator_id, project, branch, worktree_dir, tmux_session,
         state, brief_path, pr_url, NULL, created_at, updated_at, ended_at
  FROM features;

DROP TABLE features;
ALTER TABLE features_new RENAME TO features;

-- Rate-limit state for the lazy reconciler. Single-row singleton.
CREATE TABLE IF NOT EXISTS reconciler_state (
  id          INTEGER PRIMARY KEY CHECK (id = 1),
  last_run_at INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO reconciler_state (id, last_run_at) VALUES (1, 0);

INSERT INTO schema_version (version, applied_at) VALUES (2, unixepoch());
