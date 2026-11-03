-- Memory uses: the memory examples that the prompt of each run showed, so a
-- reader can tell what the model saw, and an admin who finds a bad entry can
-- tell which investigations it reached. The investigation is the one whose
-- prompt showed the example, run is its run, and example_id is the
-- investigation the example came from, which is the key of memory_entries.
-- match says why the example was chosen, as the prompt's matches do, and
-- rank is its position in the prompt, from 1.
--
-- There is no foreign key to memory_entries: an admin may delete or disable
-- an entry, or its confirmation may be taken back, and the record of what a
-- past prompt saw must stay. The rows go with their investigation.

-- +goose Up
CREATE TABLE memory_uses (
  investigation_id uuid     NOT NULL REFERENCES investigations (id) ON DELETE CASCADE,
  run              integer  NOT NULL,
  example_id       uuid     NOT NULL,
  match            text     NOT NULL CHECK (match IN ('fingerprint', 'service_alert', 'text', 'similar')),
  rank             smallint NOT NULL CHECK (rank >= 1),
  -- Also the index that reads the uses of one run of an investigation.
  PRIMARY KEY (investigation_id, run, example_id)
);

-- +goose Down
DROP TABLE memory_uses;
