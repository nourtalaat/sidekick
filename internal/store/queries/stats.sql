-- name: QualityTotals :one
-- QualityTotals counts the investigations created on the days from_day to
-- to_day, both included, that people rated, and those whose verdict was
-- confirmed as given. The range on day uses the primary key of
-- quality_daily; the filters are checked on the rows of the range.
SELECT coalesce(sum(n), 0)::bigint AS rated,
       coalesce(sum(n) FILTER (WHERE predicted = confirmed), 0)::bigint AS correct
FROM quality_daily
WHERE day >= @from_day::date AND day <= @to_day::date
  AND (sqlc.narg(route)::text IS NULL OR route = sqlc.narg(route))
  AND (sqlc.narg(service)::text IS NULL OR service = sqlc.narg(service))
  AND (sqlc.narg(model)::text IS NULL OR model = sqlc.narg(model))
  AND (sqlc.narg(verdict)::text IS NULL OR predicted = sqlc.narg(verdict));

-- name: QualityByDay :many
-- QualityByDay is QualityTotals for each day of the range.
SELECT day, sum(n)::bigint AS rated, coalesce(sum(n) FILTER (WHERE predicted = confirmed), 0)::bigint AS correct
FROM quality_daily
WHERE day >= @from_day::date AND day <= @to_day::date
  AND (sqlc.narg(route)::text IS NULL OR route = sqlc.narg(route))
  AND (sqlc.narg(service)::text IS NULL OR service = sqlc.narg(service))
  AND (sqlc.narg(model)::text IS NULL OR model = sqlc.narg(model))
  AND (sqlc.narg(verdict)::text IS NULL OR predicted = sqlc.narg(verdict))
GROUP BY day
ORDER BY day;

-- name: QualityMatrix :many
-- QualityMatrix counts the investigations of the range by the verdict
-- given and the verdict confirmed.
SELECT predicted, confirmed, sum(n)::bigint AS n
FROM quality_daily
WHERE day >= @from_day::date AND day <= @to_day::date
  AND (sqlc.narg(route)::text IS NULL OR route = sqlc.narg(route))
  AND (sqlc.narg(service)::text IS NULL OR service = sqlc.narg(service))
  AND (sqlc.narg(model)::text IS NULL OR model = sqlc.narg(model))
  AND (sqlc.narg(verdict)::text IS NULL OR predicted = sqlc.narg(verdict))
GROUP BY predicted, confirmed
ORDER BY predicted, confirmed;

-- name: QualityComparison :many
-- QualityComparison is QualityTotals for each model and strategy of the
-- range.
SELECT model, strategy, sum(n)::bigint AS rated, coalesce(sum(n) FILTER (WHERE predicted = confirmed), 0)::bigint AS correct
FROM quality_daily
WHERE day >= @from_day::date AND day <= @to_day::date
  AND (sqlc.narg(route)::text IS NULL OR route = sqlc.narg(route))
  AND (sqlc.narg(service)::text IS NULL OR service = sqlc.narg(service))
  AND (sqlc.narg(model)::text IS NULL OR model = sqlc.narg(model))
  AND (sqlc.narg(verdict)::text IS NULL OR predicted = sqlc.narg(verdict))
GROUP BY model, strategy
ORDER BY model, strategy;

-- name: StatsFilterRoutes :many
-- StatsFilterRoutes lists the routes of the hours from since on.
SELECT DISTINCT route FROM stats_hourly WHERE hour >= @since ORDER BY route;

-- name: StatsFilterServices :many
-- StatsFilterServices lists the services of the hours from since on,
-- without the empty one of investigations that have none.
SELECT DISTINCT service FROM stats_hourly WHERE hour >= @since AND service <> '' ORDER BY service;

-- name: StatsFilterModels :many
-- StatsFilterModels lists the models of the hours from since on, without
-- the empty one of investigations that have none.
SELECT DISTINCT model FROM stats_hourly WHERE hour >= @since AND model <> '' ORDER BY model;
