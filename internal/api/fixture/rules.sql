-- One runtime listener and one runtime route, for GET /config.
INSERT INTO listeners (name, slack_channel, match, trusted, enabled, version, updated_by, updated_at)
VALUES ('ops', '#ops', '{"sender": {"kind": "bot"}}', false, true, 2, 'U01SEED', '2026-10-20T10:00:00Z');

INSERT INTO routes (name, priority, spec, enabled, version, updated_by, updated_at)
VALUES ('checkout', 20, '{"match": {"service": "checkout-*"}, "mode": "read-only"}', false, 1, 'U01SEED', '2026-10-20T10:00:00Z');
