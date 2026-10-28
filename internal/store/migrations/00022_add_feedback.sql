-- Feedback: what responders say about an investigation or one part of it,
-- from Slack or the web UI. In Slack, one click on Correct or Wrong is one
-- row per user and target; clicking the other button changes that row.

-- +goose Up
CREATE TABLE feedback (
  id               uuid PRIMARY KEY,
  investigation_id uuid NOT NULL REFERENCES investigations (id) ON DELETE CASCADE,
  -- investigation, verdict, finding, tool_call or fix.
  target_kind      text NOT NULL,
  -- Which part, for example "run:1/verdict", "run:1/finding:f3" or a tool
  -- call ID.
  target_ref       text NOT NULL,
  -- correct or wrong.
  rating           text NOT NULL,
  reason           text,
  -- The verdict and cause the responder says are right, when they give
  -- them.
  correct_verdict  text,
  correct_cause    text,
  -- The Sidekick user, or for a Slack user with none yet the Slack user ID.
  user_id          uuid,
  slack_user_id    text,
  -- slack or ui.
  channel          text NOT NULL,
  created_at       timestamptz NOT NULL,
  updated_at       timestamptz NOT NULL,
  CHECK (user_id IS NOT NULL OR slack_user_id IS NOT NULL)
);

-- Finding an investigation's feedback, and the foreign key.
CREATE INDEX feedback_investigation ON feedback (investigation_id);
-- Recounting quality by day.
CREATE INDEX feedback_created ON feedback (created_at);
-- One opinion per Slack user and target.
CREATE UNIQUE INDEX feedback_slack_user_target
  ON feedback (investigation_id, target_kind, target_ref, slack_user_id)
  WHERE slack_user_id IS NOT NULL;

-- +goose Down
DROP TABLE feedback;
