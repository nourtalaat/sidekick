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

-- name: SpendTotals :one
-- SpendTotals sums what model calls cost on the days from_day to to_day,
-- both included. spend_daily has no service or verdict, so only the route
-- and model filters apply.
SELECT coalesce(sum(cost_micros), 0)::bigint AS cost_micros,
       coalesce(sum(input_tokens), 0)::bigint AS input_tokens,
       coalesce(sum(output_tokens), 0)::bigint AS output_tokens
FROM spend_daily
WHERE day >= @from_day::date AND day <= @to_day::date
  AND (sqlc.narg(route)::text IS NULL OR route = sqlc.narg(route))
  AND (sqlc.narg(model)::text IS NULL OR model = sqlc.narg(model));

-- name: SpendByDayModel :many
-- SpendByDayModel sums the spend of each day by connector and model.
SELECT day, connector, model,
       sum(cost_micros)::bigint AS cost_micros,
       sum(input_tokens)::bigint AS input_tokens,
       sum(output_tokens)::bigint AS output_tokens
FROM spend_daily
WHERE day >= @from_day::date AND day <= @to_day::date
  AND (sqlc.narg(route)::text IS NULL OR route = sqlc.narg(route))
  AND (sqlc.narg(model)::text IS NULL OR model = sqlc.narg(model))
GROUP BY day, connector, model
ORDER BY day, connector, model;

-- name: SpendByRoute :many
-- SpendByRoute sums the spend of the range by route.
SELECT route,
       sum(cost_micros)::bigint AS cost_micros,
       sum(input_tokens)::bigint AS input_tokens,
       sum(output_tokens)::bigint AS output_tokens
FROM spend_daily
WHERE day >= @from_day::date AND day <= @to_day::date
  AND (sqlc.narg(route)::text IS NULL OR route = sqlc.narg(route))
  AND (sqlc.narg(model)::text IS NULL OR model = sqlc.narg(model))
GROUP BY route
ORDER BY route;

-- name: CostPerInvestigationByDay :many
-- CostPerInvestigationByDay sums the investigations created in the hours
-- from from_hour up to to_hour, and what they cost, by UTC day. Every
-- filter applies.
SELECT (hour AT TIME ZONE 'UTC')::date AS day,
       sum(investigations)::bigint AS investigations,
       sum(cost_micros)::bigint AS cost_micros
FROM stats_hourly
WHERE hour >= @from_hour AND hour < @to_hour
  AND (sqlc.narg(route)::text IS NULL OR route = sqlc.narg(route))
  AND (sqlc.narg(service)::text IS NULL OR service = sqlc.narg(service))
  AND (sqlc.narg(model)::text IS NULL OR model = sqlc.narg(model))
  AND (sqlc.narg(verdict)::text IS NULL OR verdict = sqlc.narg(verdict))
GROUP BY 1
ORDER BY 1;

-- name: CostPerInvestigationTotals :one
-- CostPerInvestigationTotals is CostPerInvestigationByDay for the whole
-- range.
SELECT coalesce(sum(investigations), 0)::bigint AS investigations,
       coalesce(sum(cost_micros), 0)::bigint AS cost_micros
FROM stats_hourly
WHERE hour >= @from_hour AND hour < @to_hour
  AND (sqlc.narg(route)::text IS NULL OR route = sqlc.narg(route))
  AND (sqlc.narg(service)::text IS NULL OR service = sqlc.narg(service))
  AND (sqlc.narg(model)::text IS NULL OR model = sqlc.narg(model))
  AND (sqlc.narg(verdict)::text IS NULL OR verdict = sqlc.narg(verdict));

-- name: OperationsByBucket :many
-- OperationsByBucket sums the hourly rows of the hours from from_hour up to
-- to_hour by UTC bucket, which is a day or an hour as bucket says, and by
-- verdict. Every filter applies.
SELECT date_trunc(@bucket::text, hour AT TIME ZONE 'UTC')::timestamp AS bucket,
       verdict,
       sum(investigations)::bigint AS investigations,
       sum(finished)::bigint AS finished,
       sum(duration_n)::bigint AS duration_n,
       sum(duration_ms_sum)::bigint AS duration_ms_sum,
       sum(first_finding_n)::bigint AS first_finding_n,
       sum(first_finding_ms_sum)::bigint AS first_finding_ms_sum,
       sum(tool_calls)::bigint AS tool_calls,
       sum(noisy_fixed)::bigint AS noisy_fixed,
       sum(actions_proposed)::bigint AS actions_proposed,
       sum(actions_approved)::bigint AS actions_approved,
       sum(actions_executed)::bigint AS actions_executed,
       sum(recoveries)::bigint AS recoveries,
       sum(downtime_ms_sum)::bigint AS downtime_ms_sum,
       sum(watch_timeouts)::bigint AS watch_timeouts
FROM stats_hourly
WHERE hour >= @from_hour AND hour < @to_hour
  AND (sqlc.narg(route)::text IS NULL OR route = sqlc.narg(route))
  AND (sqlc.narg(service)::text IS NULL OR service = sqlc.narg(service))
  AND (sqlc.narg(model)::text IS NULL OR model = sqlc.narg(model))
  AND (sqlc.narg(verdict)::text IS NULL OR verdict = sqlc.narg(verdict))
GROUP BY 1, 2
ORDER BY 1, 2;

-- name: FirstFindingHistogram :many
-- FirstFindingHistogram adds up the histograms of the time to the first
-- finding over the hours from from_hour up to to_hour, one row for each of
-- their 24 buckets that has a count. Every filter applies.
SELECT h.i::int AS bucket, sum(h.n)::bigint AS n
FROM stats_hourly s,
     unnest(s.first_finding_hist) WITH ORDINALITY AS h(n, i)
WHERE s.hour >= @from_hour AND s.hour < @to_hour
  AND s.first_finding_n > 0
  AND (sqlc.narg(route)::text IS NULL OR s.route = sqlc.narg(route))
  AND (sqlc.narg(service)::text IS NULL OR s.service = sqlc.narg(service))
  AND (sqlc.narg(model)::text IS NULL OR s.model = sqlc.narg(model))
  AND (sqlc.narg(verdict)::text IS NULL OR s.verdict = sqlc.narg(verdict))
GROUP BY h.i
HAVING sum(h.n) > 0
ORDER BY h.i;

-- name: DowntimeHistogram :many
-- DowntimeHistogram is FirstFindingHistogram for the downtime.
SELECT h.i::int AS bucket, sum(h.n)::bigint AS n
FROM stats_hourly s,
     unnest(s.downtime_hist) WITH ORDINALITY AS h(n, i)
WHERE s.hour >= @from_hour AND s.hour < @to_hour
  AND s.recoveries > 0
  AND (sqlc.narg(route)::text IS NULL OR s.route = sqlc.narg(route))
  AND (sqlc.narg(service)::text IS NULL OR s.service = sqlc.narg(service))
  AND (sqlc.narg(model)::text IS NULL OR s.model = sqlc.narg(model))
  AND (sqlc.narg(verdict)::text IS NULL OR s.verdict = sqlc.narg(verdict))
GROUP BY h.i
HAVING sum(h.n) > 0
ORDER BY h.i;

-- name: DowntimeByRouteService :many
-- DowntimeByRouteService sums the recoveries, the downtime and the timed
-- out watchers of the range by route and service. Every filter applies.
SELECT route, service,
       sum(recoveries)::bigint AS recoveries,
       sum(downtime_ms_sum)::bigint AS downtime_ms_sum,
       sum(watch_timeouts)::bigint AS watch_timeouts
FROM stats_hourly
WHERE hour >= @from_hour AND hour < @to_hour
  AND (recoveries > 0 OR watch_timeouts > 0)
  AND (sqlc.narg(route)::text IS NULL OR route = sqlc.narg(route))
  AND (sqlc.narg(service)::text IS NULL OR service = sqlc.narg(service))
  AND (sqlc.narg(model)::text IS NULL OR model = sqlc.narg(model))
  AND (sqlc.narg(verdict)::text IS NULL OR verdict = sqlc.narg(verdict))
GROUP BY route, service
ORDER BY route, service;

-- name: DowntimeHistograms :many
-- DowntimeHistograms adds up the downtime histogram of the range by route
-- and service, one row for each of its buckets that has a count. Every
-- filter applies.
SELECT s.route, s.service, h.i::int AS bucket, sum(h.n)::bigint AS n
FROM stats_hourly s,
     unnest(s.downtime_hist) WITH ORDINALITY AS h(n, i)
WHERE s.hour >= @from_hour AND s.hour < @to_hour
  AND s.recoveries > 0
  AND (sqlc.narg(route)::text IS NULL OR s.route = sqlc.narg(route))
  AND (sqlc.narg(service)::text IS NULL OR s.service = sqlc.narg(service))
  AND (sqlc.narg(model)::text IS NULL OR s.model = sqlc.narg(model))
  AND (sqlc.narg(verdict)::text IS NULL OR s.verdict = sqlc.narg(verdict))
GROUP BY s.route, s.service, h.i
HAVING sum(h.n) > 0
ORDER BY s.route, s.service, h.i;
