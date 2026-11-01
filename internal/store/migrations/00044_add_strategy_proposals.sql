-- Proposed strategy changes. After a strategy version collects enough wrong
-- feedback, a job drafts a changed strategy and a second job runs the
-- benchmark gate on it: the current version and the proposal each run the
-- scenarios that cover the strategy, and the proposal passes only when it is
-- not clearly worse. Only a passed proposal can become a strategy version.
--
-- A strategy version gets one proposal, ever: the unique index on (name,
-- base_version) is what makes the trigger fire once. The checks on the table
-- keep an approved or applied proposal tied to a passed gate, so no code path
-- can apply one that did not pass.
--
-- strategy_gate_runs holds the runs of a gate that have finished, so a gate
-- that is interrupted carries on from there instead of paying for them again.

-- +goose Up
CREATE TABLE strategy_proposals (
  id             uuid PRIMARY KEY,
  name           text NOT NULL,
  -- The version the draft started from, as strategy.Version spells it:
  -- runtime:<n> for a runtime strategy, or a hash for a file or built-in one.
  base_version   text NOT NULL,
  -- built-in, file or runtime. Null while drafting.
  base_origin    text,
  -- The base version's YAML and the proposed YAML. Null while drafting.
  base_body      text,
  body           text,
  rationale      text,
  -- The wrong feedback the draft was made from.
  feedback_ids   uuid[] NOT NULL DEFAULT '{}',
  trigger_count  int NOT NULL,
  -- drafting, draft_failed, pending, benchmarking, passed, failed, approved,
  -- rejected or applied.
  status         text NOT NULL,
  -- Why a draft failed.
  status_detail  text,
  -- passed, failed, not_enough_evidence, no_benchmark_coverage, over_budget
  -- or error. Null until the gate ends.
  gate_result    text,
  -- The mean difference, its 95% interval, the settings, the seed and the
  -- result of each scenario.
  gate_detail    jsonb,
  -- A hash of the scenarios the gate runs, to tell a retry on the same
  -- scenarios from one after the set changed.
  gate_set_hash  text,
  -- The benchmark run of the proposal and of the version it is compared with.
  -- A run that proves a decision cannot be deleted.
  bench_run_id    uuid REFERENCES bench_runs (id),
  baseline_run_id uuid REFERENCES bench_runs (id),
  -- The user or API token that approved, rejected or last edited it.
  reviewer       uuid,
  created_at     timestamptz NOT NULL,
  updated_at     timestamptz NOT NULL,
  decided_at     timestamptz,
  CONSTRAINT strategy_proposals_status CHECK (status IN
    ('drafting', 'draft_failed', 'pending', 'benchmarking', 'passed', 'failed', 'approved', 'rejected', 'applied')),
  CONSTRAINT strategy_proposals_gate_result CHECK (gate_result IS NULL OR gate_result IN
    ('passed', 'failed', 'not_enough_evidence', 'no_benchmark_coverage', 'over_budget', 'error')),
  CONSTRAINT strategy_proposals_drafted CHECK
    (status IN ('drafting', 'draft_failed') OR (body IS NOT NULL AND base_body IS NOT NULL AND base_origin IS NOT NULL AND rationale IS NOT NULL)),
  CONSTRAINT strategy_proposals_passed CHECK (status <> 'passed' OR gate_result = 'passed'),
  CONSTRAINT strategy_proposals_failed CHECK
    (status <> 'failed' OR (gate_result IS NOT NULL AND gate_result <> 'passed')),
  CONSTRAINT strategy_proposals_applied CHECK
    (status NOT IN ('approved', 'applied')
     OR (gate_result = 'passed' AND bench_run_id IS NOT NULL AND baseline_run_id IS NOT NULL))
);

-- One proposal per strategy version, and finding the proposal of a version.
CREATE UNIQUE INDEX strategy_proposals_version ON strategy_proposals (name, base_version);
-- The review list, by status, newest first.
CREATE INDEX strategy_proposals_status_created ON strategy_proposals (status, created_at DESC, id DESC);
-- The review list without a status filter.
CREATE INDEX strategy_proposals_created ON strategy_proposals (created_at DESC, id DESC);

CREATE TABLE strategy_gate_runs (
  proposal_id uuid NOT NULL REFERENCES strategy_proposals (id) ON DELETE CASCADE,
  -- baseline or proposal.
  arm         text NOT NULL CHECK (arm IN ('baseline', 'proposal')),
  scenario_id text NOT NULL,
  repeat      int NOT NULL,
  -- The run's result as the benchmark runner returns it, before scoring.
  result      jsonb NOT NULL,
  cost_micros bigint NOT NULL,
  created_at  timestamptz NOT NULL,
  PRIMARY KEY (proposal_id, arm, scenario_id, repeat)
);

-- The links SK-43 left out: a version made from a proposal, and the run that
-- checked it.
ALTER TABLE strategy_versions
  ADD CONSTRAINT strategy_versions_proposal FOREIGN KEY (proposal_id) REFERENCES strategy_proposals (id),
  ADD CONSTRAINT strategy_versions_bench_run FOREIGN KEY (bench_run_id) REFERENCES bench_runs (id);

-- Counting the wrong ratings of the last days when someone rates a verdict or
-- finding wrong.
CREATE INDEX feedback_wrong_recent ON feedback (updated_at)
  WHERE rating = 'wrong' AND target_kind IN ('verdict', 'finding');

-- Finding the confirmed investigations of a strategy for a draft.
CREATE INDEX investigations_strategy_confirmed ON investigations (strategy, confirmed_at DESC)
  WHERE confirmed_verdict IS NOT NULL;

-- +goose Down
DROP INDEX investigations_strategy_confirmed;
DROP INDEX feedback_wrong_recent;
ALTER TABLE strategy_versions
  DROP CONSTRAINT strategy_versions_bench_run,
  DROP CONSTRAINT strategy_versions_proposal;
DROP TABLE strategy_gate_runs;
DROP TABLE strategy_proposals;
