-- Rules added from Slack before each test: the listener billing and the
-- route checkout.
INSERT INTO listeners (name, slack_channel, match, trusted, enabled, version, updated_by, updated_at)
VALUES ('billing', 'C01BILLING', '{"sender": {"kind": "bot", "names": ["Grafana"]}}', false, true, 1, 'U01SEED', '2026-10-20T10:00:00Z');

INSERT INTO routes (name, priority, spec, enabled, version, updated_by, updated_at)
VALUES ('checkout', 20, '{"match": {"service": "checkout-*"}, "strategy": "generic-alert", "mode": "read-only", "grouping": {"window": "30m0s"}}',
        true, 1, 'U01SEED', '2026-10-20T10:00:00Z');
