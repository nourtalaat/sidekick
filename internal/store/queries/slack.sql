-- name: TryTakeSlackSlot :one
-- TryTakeSlackSlot takes the session advisory lock of one Socket Mode
-- connection slot, and reports whether this session now holds it. The lock
-- lasts until the session ends, so a replica that dies frees its slot.
SELECT pg_try_advisory_lock(sqlc.arg(lock_class)::integer, sqlc.arg(slot)::integer) AS taken;
