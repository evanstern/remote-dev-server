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

-- Features: only add stale_reason. 'failed' is already in the CHECK.
ALTER TABLE features ADD COLUMN stale_reason TEXT;

INSERT INTO schema_version (version, applied_at) VALUES (2, unixepoch());
