-- Rate admissions: the events each rate limit let through, with when, so
-- the router can count the events of the last window. The router keeps
-- only the rows of the current window, so the table stays small.

-- +goose Up
CREATE TABLE rate_admissions (
  -- global, or route:<name>
  scope       text NOT NULL,
  event_id    uuid NOT NULL,
  admitted_at timestamptz NOT NULL,
  PRIMARY KEY (scope, event_id)
);

-- For counting a scope's admissions in its window.
CREATE INDEX rate_admissions_scope_admitted ON rate_admissions (scope, admitted_at);

-- +goose Down
DROP TABLE rate_admissions;
