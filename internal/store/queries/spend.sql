-- name: AddSpend :exec
-- AddSpend adds the cost and tokens of one model call to the day's spend of
-- its route, connector and model. Run it in the transaction that saves the
-- step.
INSERT INTO spend_daily (day, route, connector, model, cost_micros, input_tokens, output_tokens)
VALUES (@day, @route, @connector, @model, @cost_micros, @input_tokens, @output_tokens)
ON CONFLICT (day, route, connector, model) DO UPDATE
SET cost_micros = spend_daily.cost_micros + excluded.cost_micros,
    input_tokens = spend_daily.input_tokens + excluded.input_tokens,
    output_tokens = spend_daily.output_tokens + excluded.output_tokens;

-- name: GetDailySpend :one
-- GetDailySpend returns what one route and every route together spent on
-- a day, in micro-dollars.
SELECT coalesce(sum(cost_micros) FILTER (WHERE route = @route), 0)::bigint AS route_micros,
       coalesce(sum(cost_micros), 0)::bigint AS total_micros
FROM spend_daily
WHERE day = @day;

-- name: ListDailySpend :many
-- ListDailySpend returns what each route spent on a day, in micro-dollars.
SELECT route, sum(cost_micros)::bigint AS cost_micros
FROM spend_daily
WHERE day = @day
GROUP BY route
ORDER BY route;
