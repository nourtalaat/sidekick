-- name: InsertInvestigation :exec
-- InsertInvestigation creates an investigation for a group.
INSERT INTO investigations (
  id, group_id, route, strategy, strategy_version, mode, status, connectors, budget, created_at
)
VALUES (
  @id, @group_id, @route, @strategy, @strategy_version, @mode, @status, @connectors, @budget, now()
);

-- name: GetInvestigationWithEvent :one
-- GetInvestigationWithEvent returns an investigation with the first event
-- of its group, the one that opened it.
SELECT sqlc.embed(investigations), sqlc.embed(events)
FROM investigations
JOIN events ON events.group_id = investigations.group_id
WHERE investigations.id = @id
ORDER BY events.received_at, events.id
LIMIT 1;
