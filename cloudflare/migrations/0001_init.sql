CREATE TABLE IF NOT EXISTS handoffs (
  id TEXT PRIMARY KEY,
  payload TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS handoffs_expires_at_idx ON handoffs (expires_at);
