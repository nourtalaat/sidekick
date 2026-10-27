-- Notices: when each kind of notice was last sent, by key, so a notice
-- such as "daily cap reached" goes out at most once per window. Claiming a
-- key is one upsert, so two replicas cannot both send the same notice.

-- +goose Up
CREATE TABLE notices (
  -- For example daily_cap:route:payments or rate_limit:global.
  key     text PRIMARY KEY,
  sent_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE notices;
