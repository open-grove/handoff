CREATE TABLE IF NOT EXISTS handoff_context_chunks (
  handoff_id TEXT NOT NULL,
  chunk_index INTEGER NOT NULL,
  payload TEXT NOT NULL,
  PRIMARY KEY (handoff_id, chunk_index)
);

CREATE INDEX IF NOT EXISTS handoff_context_chunks_handoff_id_idx
  ON handoff_context_chunks (handoff_id);
