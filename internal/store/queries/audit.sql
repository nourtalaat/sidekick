-- name: InsertAuditEntry :exec
-- InsertAuditEntry adds one row to the audit log. Run it in the
-- transaction that makes the change it records.
INSERT INTO audit_log (at, actor_kind, actor_id, action, target_kind, target_id, investigation_id, details)
VALUES (@at, @actor_kind, @actor_id, @action, @target_kind, @target_id, @investigation_id, @details);
