-- Web UI users and their sessions. A user is who the OIDC provider says
-- signed in, kept by issuer and subject; the role comes from their groups
-- and is set again at every login. A session's ID lives only in the
-- browser's cookie: the table keeps its SHA-256 hash.

-- +goose Up
CREATE TABLE users (
  id            uuid PRIMARY KEY,
  oidc_issuer   text NOT NULL,
  oidc_subject  text NOT NULL,
  -- Only a verified email, for matching Slack users. One user holds an
  -- email at a time: a newer login with it takes it over.
  email         text,
  name          text NOT NULL,
  groups        text[] NOT NULL DEFAULT '{}',
  -- viewer, responder, approver or admin, from the groups at the last
  -- login.
  role          text NOT NULL,
  created_at    timestamptz NOT NULL,
  last_login_at timestamptz NOT NULL,
  CONSTRAINT users_subject UNIQUE (oidc_issuer, oidc_subject)
);

CREATE UNIQUE INDEX users_email ON users (lower(email));

CREATE TABLE sessions (
  id_hash    bytea PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  -- Sent back in the X-CSRF-Token header of requests that change
  -- something.
  csrf_token text NOT NULL,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL
);

CREATE INDEX sessions_expires_at ON sessions (expires_at);

-- +goose Down
DROP TABLE sessions;
DROP TABLE users;
