-- This fails if it runs twice, because the key is already taken.

-- +goose Up
INSERT INTO migrated_once (id) VALUES (1);

-- +goose Down
DELETE FROM migrated_once WHERE id = 1;
