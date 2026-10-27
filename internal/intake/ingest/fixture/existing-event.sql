-- An event the alerts intake stored earlier with the key "seen", so storing
-- that key again is a duplicate.
INSERT INTO events (id, intake, key, kind, trusted, title, labels, fingerprint, status, received_at)
VALUES ('01900000-0000-7000-8000-000000000001', 'alerts', 'seen', 'alert', false,
        'Disk almost full', '{}', 'f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0', 'received', '2026-10-26T09:00:00Z');
