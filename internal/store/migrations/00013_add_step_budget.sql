-- The budget a note step is about. When a budget is nearly used, the
-- investigator saves a note that gives the model one last turn, with the
-- budget named here, so a resumed run knows that turn has begun.

-- +goose Up
-- tokens, tool_calls, wall_time, cost or daily_cap; NULL for other steps.
ALTER TABLE steps ADD COLUMN budget text;

-- +goose Down
ALTER TABLE steps DROP COLUMN budget;
