-- The theme a person chose in the web UI: system, light or dark. Null means
-- the person never chose one, so the browser's own choice or the system
-- setting applies. The column is new and nullable, so no existing row
-- changes.

-- +goose Up
ALTER TABLE users ADD COLUMN theme text;
ALTER TABLE users ADD CONSTRAINT users_theme_valid CHECK (theme IN ('system', 'light', 'dark'));

-- +goose Down
ALTER TABLE users DROP COLUMN theme;
