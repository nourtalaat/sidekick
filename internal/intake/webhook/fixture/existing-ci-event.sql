-- An event the ci endpoint stored earlier, with the key of
-- requests/generic-alert.json, so delivering that request again is a
-- duplicate.
INSERT INTO events (id, intake, key, kind, trusted, title, labels, fingerprint, status, received_at)
VALUES ('01900000-0000-7000-8000-000000000001', 'ci', 'build-4711', 'alert', false,
        'Nightly build failed', '{}', 'f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0', 'received', '2026-10-26T09:00:00Z');
