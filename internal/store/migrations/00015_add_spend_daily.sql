-- Daily spend: the cost and tokens of model calls, per day, route,
-- connector and model. The investigator adds each priced model step in the
-- transaction that saves the step, so the daily caps read what was spent.
-- The day is the one of budgets.daily.timeZone.

-- +goose Up
CREATE TABLE spend_daily (
  day           date NOT NULL,
  route         text NOT NULL,
  connector     text NOT NULL,
  model         text NOT NULL,
  -- Micro-dollars, from the price table; 0 for models without a price.
  cost_micros   bigint NOT NULL DEFAULT 0,
  input_tokens  bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (day, route, connector, model)
);

-- +goose Down
DROP TABLE spend_daily;
