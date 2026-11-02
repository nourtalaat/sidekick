-- name: ListInvestigationOutputRefs :many
-- ListInvestigationOutputRefs lists where the outputs of an investigation
-- published, the first to publish first. A Slack action finds the
-- investigation's thread in it. The primary key serves the lookup.
SELECT sink, external
FROM output_refs
WHERE investigation_id = @investigation_id
ORDER BY published_at, sink;

-- name: GetInvestigationAlertSlackRef :one
-- GetInvestigationAlertSlackRef returns the Slack message reference of the
-- first alert event of an investigation's group that came from Slack, which
-- is the alert message a Slack action reacts to or replies under. A message
-- that a person posted in a listener's channel counts as an alert, as does a
-- bot's; a mention of Sidekick and a recovery do not, since they are not
-- the alert. It returns no row when the alert did not come from Slack. The
-- investigation is read by its primary key and the group's events by the
-- group index.
SELECT e.slack::jsonb AS slack
FROM investigations i
JOIN events e ON e.group_id = i.group_id
WHERE i.id = @investigation_id AND e.slack IS NOT NULL AND e.kind IN ('alert', 'message')
ORDER BY e.received_at, e.id
LIMIT 1;
