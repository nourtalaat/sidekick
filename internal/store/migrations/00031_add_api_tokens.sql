-- API tokens for automation, such as uploading benchmark results from CI.
-- An admin signed in to the web UI creates them, each with a role and an
-- expiry. The token itself is shown once; the table keeps its SHA-256
-- hash.

-- +goose Up
CREATE TABLE api_tokens (
  id           uuid PRIMARY KEY,
  name         text NOT NULL,
  token_hash   bytea NOT NULL,
  -- viewer, responder, approver or admin.
  role         text NOT NULL,
  -- The web UI user who created it.
  created_by   uuid NOT NULL REFERENCES users (id),
  created_at   timestamptz NOT NULL,
  expires_at   timestamptz NOT NULL,
  -- Updated at most once a minute, so every request does not write.
  last_used_at timestamptz,
  revoked_at   timestamptz,
  CONSTRAINT api_tokens_hash UNIQUE (token_hash)
);

-- +goose Down
DROP TABLE api_tokens;
