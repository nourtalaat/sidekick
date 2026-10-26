-- name: ServerVersionNum :one
-- ServerVersionNum returns the Postgres version as a number, for example
-- 160004 for 16.4.
SELECT current_setting('server_version_num')::integer AS version_num;
