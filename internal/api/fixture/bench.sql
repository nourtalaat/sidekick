-- Two benchmark runs for the tests of the benchmark endpoints.
--
-- Run a (0192d3a4-0000-7000-8000-0000000000a1, "baseline", 2026-10-20)
-- passed: scenario s1 ran twice and both were correct (root cause scores
-- 0.9 and 0.8), scenario s2 ran twice and one was correct (one graded, 0.5).
-- Run b (0192d3a4-0000-7000-8000-0000000000a2, 2026-10-22) failed a
-- threshold: s1 ran once and was wrong (0.2), and s3, which a lacks, ran
-- once and was right (1.0).
INSERT INTO bench_runs (
  id, label, started_at, finished_at, sidekick_version, git_sha, config_hash, connectors, strategies,
  scenario_set, repeats, summary, status, triggered_by
)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a1', 'baseline', '2026-10-20 10:00:00+00', '2026-10-20 10:30:00+00', 'v0.9.0', 'abc1234', 'cfg1',
   '{"investigate": {"connector": "claude", "provider": "anthropic", "model": "big-model"}}',
   '{"generic-alert": "v1"}', 'starter', 2,
   '{"schemaVersion": 1, "promptVersion": "p1", "judgePromptVersion": "j1", "seed": 1, "resamples": 100, "confidenceLevel": 0.95,
     "weights": {"verdict": 0.5, "rootCause": 0.3, "evidence": 0.2},
     "metrics": {"runs": 4, "verdictAccuracy": 0.75, "falsePositive": {"precision": 0.8, "recall": 1, "predicted": 2, "expected": 2, "hits": 2},
                 "truePositive": {"precision": null, "recall": 0.5, "predicted": 0, "expected": 2, "hits": 1},
                 "rootCauseMatch": null, "rootCauseScore": 0.73, "evidence": null, "sourceFailuresReported": null, "overall": 0.7,
                 "judgeErrors": 0, "toolCalls": 12, "inputTokens": 1000, "outputTokens": 500, "costMicros": 8000, "judgeCostMicros": 100,
                 "meanCostPerRunUSD": 0.002, "meanDurationMs": 1500},
     "intervals": {"overall": {"lower": 0.5, "upper": 0.9}, "verdictAccuracy": {"lower": 0.4, "upper": 1}},
     "thresholds": [{"name": "overall", "limit": 0.6, "value": 0.7, "passed": true}]}',
   'passed', 'cli'),
  ('0192d3a4-0000-7000-8000-0000000000a2', NULL, '2026-10-22 10:00:00+00', '2026-10-22 10:10:00+00', 'v0.9.1', 'def5678', 'cfg2',
   '{"investigate": {"connector": "openai", "provider": "openai", "model": "small-model"}}',
   '{}', 'starter', 1,
   '{"schemaVersion": 1, "promptVersion": "p2", "judgePromptVersion": "j1", "seed": 1, "resamples": 100, "confidenceLevel": 0.95,
     "weights": {"verdict": 0.5, "rootCause": 0.3, "evidence": 0.2},
     "metrics": {"runs": 2, "verdictAccuracy": 0.5, "falsePositive": {"precision": null, "recall": 0, "predicted": 0, "expected": 1, "hits": 0},
                 "truePositive": {"precision": null, "recall": null, "predicted": 0, "expected": 0, "hits": 0},
                 "rootCauseMatch": null, "rootCauseScore": 0.6, "evidence": null, "sourceFailuresReported": null, "overall": 0.4,
                 "judgeErrors": 1, "toolCalls": 6, "inputTokens": 400, "outputTokens": 200, "costMicros": 8000, "judgeCostMicros": 50,
                 "meanCostPerRunUSD": 0.004, "meanDurationMs": 2500},
     "intervals": {"overall": {"lower": 0.2, "upper": 0.45}, "verdictAccuracy": {"lower": 0.2, "upper": 0.8}},
     "thresholds": [{"name": "overall", "limit": 0.6, "value": 0.4, "passed": false, "detail": "below the limit"}]}',
   'failed', 'ci');

INSERT INTO bench_results (
  run_id, scenario_id, repeat, verdict, expected_verdict, verdict_correct, root_cause_score, required_met, judge_reason,
  evidence_score, cost_micros, duration_ms, input_tokens, output_tokens, tool_calls
)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a1', 's1', 1, 'false_positive', 'false_positive', true, 0.9, true, 'item 1: met: secret judge text', 1.0, 2000, 1200, 250, 125, 3),
  ('0192d3a4-0000-7000-8000-0000000000a1', 's1', 2, 'false_positive', 'false_positive', true, 0.8, true, 'item 1: met', NULL, 2000, 1300, 250, 125, 3),
  ('0192d3a4-0000-7000-8000-0000000000a1', 's2', 1, 'true_positive', 'true_positive', true, 0.5, false, 'item 1: not met', NULL, 2000, 1800, 250, 125, 3),
  ('0192d3a4-0000-7000-8000-0000000000a1', 's2', 2, 'inconclusive', 'true_positive', false, NULL, NULL, NULL, NULL, 2000, 1700, 250, 125, 3),
  ('0192d3a4-0000-7000-8000-0000000000a2', 's1', 1, 'inconclusive', 'false_positive', false, 0.2, false, 'item 1: not met', NULL, 4000, 2000, 200, 100, 3),
  ('0192d3a4-0000-7000-8000-0000000000a2', 's3', 1, 'true_positive', 'true_positive', true, 1.0, true, 'item 1: met', NULL, 4000, 3000, 200, 100, 3);
