-- Five entries for the rank fusion tests, with embeddings of 3 numbers. A
-- search for "checkout latency deploy timeout" matches the summaries by
-- words, best first: f1 (all four words), f2 (three), f3 (two), f4 (one);
-- f5 has none. The query vector is [1,0,0], so the entries come by
-- distance in the order f3, f1, f5, f2, f4.
INSERT INTO memory_entries (investigation_id, fingerprint, verdict, summary, confirmed_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000f1', 'fp-f1', 'true_positive', 'checkout latency deploy timeout', '2026-10-20 10:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000f2', 'fp-f2', 'true_positive', 'checkout latency deploy', '2026-10-20 10:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000f3', 'fp-f3', 'true_positive', 'checkout latency', '2026-10-20 10:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000f4', 'fp-f4', 'true_positive', 'checkout', '2026-10-20 10:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000f5', 'fp-f5', 'true_positive', 'batch export disk', '2026-10-20 10:00:00+00');

INSERT INTO memory_embeddings (investigation_id, model, dimensions, embedding, embedded_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000f1', 'test-model', 3, '[1,0.1,0]', now()),
  ('0192d3a4-0000-7000-8000-0000000000f2', 'test-model', 3, '[1,0.6,0]', now()),
  ('0192d3a4-0000-7000-8000-0000000000f3', 'test-model', 3, '[1,0,0]', now()),
  ('0192d3a4-0000-7000-8000-0000000000f4', 'test-model', 3, '[1,1.5,0]', now()),
  ('0192d3a4-0000-7000-8000-0000000000f5', 'test-model', 3, '[1,0.3,0]', now());
