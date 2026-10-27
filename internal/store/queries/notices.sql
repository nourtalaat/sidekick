-- name: ClaimNotice :one
-- ClaimNotice records that the notice with key is sent at sent_at, unless
-- it was sent at or after not_before. It returns the key when the caller
-- should send the notice, and no row when it was sent already. The
-- upsert locks the row, so of two claims at once only one returns it.
INSERT INTO notices (key, sent_at)
VALUES (@key, @sent_at)
ON CONFLICT (key) DO UPDATE SET sent_at = excluded.sent_at
WHERE notices.sent_at < @not_before
RETURNING key;
