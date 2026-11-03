-- One investigation for each way a false positive can, or cannot, count as
-- a noisy alert fixed. The service of an investigation is the name of its
-- case, so each case has a row of stats_hourly of its own. All are in the
-- hour 2026-10-20 10:00 UTC, except late_merge.
--
-- Counted, as 1:
--   applied_only          a responder marked the fix applied
--   merged_only           its pull request proposal succeeded and is merged
--   api_only              its API proposal succeeded
--   all_three             all of the above at once
--   two_api_and_merged    two API proposals and a merged pull request
--   late_merge            created on 2026-10-21 at 09:15, merged on the 25th:
--                         counts in the hour it was created in
--
-- Not counted, as 0:
--   merged_undone         a merged pull request whose proposal was undone
--   api_undone            an API change that was undone (the undo is a
--                         succeeded proposal with no fix kind)
--   pr_open, pr_closed    pull requests that are not merged
--   pr_no_row             a succeeded pull request proposal that the daily
--                         check has not read yet
--   pr_failed             a pull request proposal that failed
--   api_failed            API proposals that failed or wait
--   not_false_positive    a true positive with all three
--   no_verdict            a running investigation with a fix marked
--   mute                  a succeeded proposal that fixes no monitor
--   code_change           a merged pull request of a proposal with no fix kind
CREATE TEMP TABLE cases (name text, verdict text, status text, applied boolean, created timestamptz);

INSERT INTO cases VALUES
  ('applied_only',       'false_positive', 'final',   true,  '2026-10-20 10:01:00+00'),
  ('merged_only',        'false_positive', 'final',   false, '2026-10-20 10:02:00+00'),
  ('api_only',           'false_positive', 'final',   false, '2026-10-20 10:03:00+00'),
  ('all_three',          'false_positive', 'final',   true,  '2026-10-20 10:04:00+00'),
  ('two_api_and_merged', 'false_positive', 'final',   false, '2026-10-20 10:05:00+00'),
  ('late_merge',         'false_positive', 'final',   false, '2026-10-21 09:15:00+00'),
  ('merged_undone',      'false_positive', 'final',   false, '2026-10-20 10:06:00+00'),
  ('api_undone',         'false_positive', 'final',   false, '2026-10-20 10:07:00+00'),
  ('pr_open',            'false_positive', 'final',   false, '2026-10-20 10:08:00+00'),
  ('pr_closed',          'false_positive', 'final',   false, '2026-10-20 10:09:00+00'),
  ('pr_no_row',          'false_positive', 'final',   false, '2026-10-20 10:10:00+00'),
  ('pr_failed',          'false_positive', 'final',   false, '2026-10-20 10:11:00+00'),
  ('api_failed',         'false_positive', 'final',   false, '2026-10-20 10:12:00+00'),
  ('not_false_positive', 'true_positive',  'final',   true,  '2026-10-20 10:13:00+00'),
  ('no_verdict',         NULL,             'running', true,  '2026-10-20 10:14:00+00'),
  ('mute',               'false_positive', 'final',   false, '2026-10-20 10:15:00+00'),
  ('code_change',        'false_positive', 'final',   false, '2026-10-20 10:16:00+00');

-- The proposals of each case. pr_state is the state of its pull request, and
-- null when it has no row in pull_request_states.
CREATE TEMP TABLE fixes (case_name text, n int, status text, fix_kind text, pr_state text);

INSERT INTO fixes VALUES
  ('merged_only',        1, 'succeeded', 'pull_request', 'merged'),
  ('api_only',           1, 'succeeded', 'api',          NULL),
  ('all_three',          1, 'succeeded', 'api',          NULL),
  ('all_three',          2, 'succeeded', 'pull_request', 'merged'),
  ('two_api_and_merged', 1, 'succeeded', 'api',          NULL),
  ('two_api_and_merged', 2, 'succeeded', 'api',          NULL),
  ('two_api_and_merged', 3, 'succeeded', 'pull_request', 'merged'),
  ('late_merge',         1, 'succeeded', 'pull_request', 'merged'),
  ('merged_undone',      1, 'undone',    'pull_request', 'merged'),
  ('api_undone',         1, 'undone',    'api',          NULL),
  ('pr_open',            1, 'succeeded', 'pull_request', 'open'),
  ('pr_closed',          1, 'succeeded', 'pull_request', 'closed'),
  ('pr_no_row',          1, 'succeeded', 'pull_request', NULL),
  ('pr_failed',          1, 'failed',    'pull_request', 'merged'),
  ('api_failed',         1, 'failed',    'api',          NULL),
  ('api_failed',         2, 'proposed',  'api',          NULL),
  ('not_false_positive', 1, 'succeeded', 'api',          NULL),
  ('not_false_positive', 2, 'succeeded', 'pull_request', 'merged'),
  ('no_verdict',         1, 'succeeded', 'api',          NULL),
  ('mute',               1, 'succeeded', NULL,           NULL),
  ('code_change',        1, 'succeeded', NULL,           'merged');

INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, investigation_id)
SELECT md5('group-' || name)::uuid, 'fp-' || name, 'r1', 'closed', 1, created, created, md5('investigation-' || name)::uuid
FROM cases;

INSERT INTO investigations (id, group_id, route, mode, status, verdict, connectors, budget, service, created_at, fix_applied_at)
SELECT md5('investigation-' || name)::uuid, md5('group-' || name)::uuid, 'r1', 'read-only', status, verdict, '{}', '{}', name, created,
       CASE WHEN applied THEN created + interval '30 minutes' END
FROM cases;

INSERT INTO action_proposals (id, investigation_id, action, params, mode, status, idempotency_key, proposed_at, expires_at, fix_kind)
SELECT md5('proposal-' || f.case_name || f.n)::uuid, md5('investigation-' || f.case_name)::uuid, 'note', '{}', 'approved', f.status,
       'k-' || f.case_name || f.n, c.created + interval '1 minute', c.created + interval '1 day', f.fix_kind
FROM fixes f
JOIN cases c ON c.name = f.case_name;

-- The undo of the API change of api_undone, which fixes nothing itself.
INSERT INTO action_proposals (id, investigation_id, action, params, mode, status, idempotency_key, proposed_at, expires_at, undo_of)
SELECT md5('undo-' || name)::uuid, md5('investigation-' || name)::uuid, 'note', '{}', 'approved', 'succeeded',
       'k-undo-' || name, created + interval '10 minutes', created + interval '1 day', md5('proposal-' || name || 1)::uuid
FROM cases
WHERE name = 'api_undone';

INSERT INTO pull_request_states (proposal_id, repo, number, url, state, merged_at, closed_at, checked_at)
SELECT md5('proposal-' || f.case_name || f.n)::uuid, 'acme/monitors', 1, 'https://github.com/acme/monitors/pull/1', f.pr_state,
       CASE WHEN f.pr_state = 'merged' THEN c.created + CASE WHEN f.case_name = 'late_merge' THEN interval '4 days' ELSE interval '1 hour' END END,
       CASE WHEN f.pr_state = 'closed' THEN c.created + interval '1 hour' END,
       c.created + interval '5 days'
FROM fixes f
JOIN cases c ON c.name = f.case_name
WHERE f.pr_state IS NOT NULL;

DROP TABLE fixes;
DROP TABLE cases;
