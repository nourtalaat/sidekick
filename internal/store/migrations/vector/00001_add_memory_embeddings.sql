-- Vector memory: one embedding for each memory entry, so a search can find
-- entries that mean the same as an alert in other words.
--
-- This set runs only when memory.vector.enabled is true. The embedding
-- column has no length here, because the length is set per install by the
-- dimensions of the embeddings model: store.MigrateVector gives the column
-- its length and builds the HNSW index right after this migration.
--
-- Rows are derived data. An entry's row goes with the entry, and a changed
-- entry is embedded again.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE memory_embeddings (
  investigation_id uuid PRIMARY KEY REFERENCES memory_entries (investigation_id) ON DELETE CASCADE,
  -- The embeddings model, so a changed model embeds the entry again.
  model            text NOT NULL,
  dimensions       int NOT NULL,
  embedding        vector NOT NULL,
  embedded_at      timestamptz NOT NULL
);

-- +goose Down
DROP TABLE memory_embeddings;
