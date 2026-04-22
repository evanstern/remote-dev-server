-- Typed message bus: durability layer for coda send/recv/ack.
-- Recipient may be an orchestrator name or the literal 'broadcast'
-- (broadcast semantics parked per design doc §Open questions §1).
-- parent_id enables threading (e.g. note replying to an escalation).
CREATE TABLE IF NOT EXISTS messages (
  id            INTEGER PRIMARY KEY,
  sender        TEXT NOT NULL,
  recipient     TEXT NOT NULL,
  type          TEXT NOT NULL
                CHECK (type IN ('brief','status','completion','escalation','note')),
  body          TEXT NOT NULL,
  parent_id     INTEGER REFERENCES messages(id) ON DELETE SET NULL,
  created_at    INTEGER NOT NULL,
  delivered_at  INTEGER,
  acked_at      INTEGER
);

-- Undelivered queue lookup (coda drain).
CREATE INDEX IF NOT EXISTS idx_messages_undelivered
  ON messages(recipient, created_at)
  WHERE delivered_at IS NULL;

-- Unacked inbox lookup (coda recv --unacked, coda status).
CREATE INDEX IF NOT EXISTS idx_messages_unacked
  ON messages(recipient, created_at)
  WHERE acked_at IS NULL;

-- Thread lookup.
CREATE INDEX IF NOT EXISTS idx_messages_parent
  ON messages(parent_id)
  WHERE parent_id IS NOT NULL;

INSERT INTO schema_version (version, applied_at) VALUES (3, unixepoch());
