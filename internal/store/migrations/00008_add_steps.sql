-- Steps: the saved record of an investigation run, one row per prompt,
-- model turn, tool result and note, in order. A run that stops part way,
-- after a crash or a lost lease, continues from its last saved step.

-- +goose Up
CREATE TABLE steps (
  investigation_id   uuid NOT NULL REFERENCES investigations (id) ON DELETE CASCADE,
  -- 0, 1, 2 and so on, with no gaps. Each write takes investigations.next_seq
  -- in the same transaction, fenced by the claim token.
  seq                int NOT NULL,
  run                int NOT NULL,
  -- prompt, model_turn, tool_result or note.
  kind               text NOT NULL,
  -- The step's content: the model.Message parts of a turn, a tool result or
  -- a note; for the prompt, the system text, the tools and the first user
  -- message. Tool results are redacted and wrapped as untrusted data.
  parts              jsonb NOT NULL,
  -- For tool results: the source and its own tool name (NULL for built-in
  -- tools), and the ID of the call the result answers.
  source             text,
  tool               text,
  tool_call_id       text,
  -- ok, no_data, error, timeout, unavailable or rejected.
  status             text NOT NULL,
  error              text,
  -- The source's evidence links, as []source.Evidence.
  evidence           jsonb,
  -- For model turns: the connector, the model, the tokens and the cost in
  -- micro-dollars, NULL when the model has no price.
  connector          text,
  model              text,
  input_tokens       bigint,
  output_tokens      bigint,
  cache_read_tokens  bigint,
  cache_write_tokens bigint,
  cost_micros        bigint,
  started_at         timestamptz NOT NULL,
  duration_ms        int NOT NULL,
  PRIMARY KEY (investigation_id, seq)
);

-- +goose Down
DROP TABLE steps;
