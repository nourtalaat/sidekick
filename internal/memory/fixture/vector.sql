-- Six entries for the rank fusion tests, with embeddings of 3 numbers. A
-- search for "checkout latency deploy timeout" matches the summaries by
-- words, best first: f1 (all four words), f2 (three), f3 (two), f4 (one);
-- f5 and f6 have none. The query vector is [1,0,0], so the entries of the
-- current model come by distance in the order f3, f1, f5, f2, f4.
--
-- f6 is the nearest of all, but its vector is from another model, so a
-- search never returns it. f3 and f5 are of the service billing, and f5
-- has the verdict false_positive; the others are true_positive.
INSERT INTO memory_entries (investigation_id, service, fingerprint, verdict, summary, confirmed_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000f1', NULL, 'fp-f1', 'true_positive', 'checkout latency deploy timeout', '2026-10-20 10:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000f2', NULL, 'fp-f2', 'true_positive', 'checkout latency deploy', '2026-10-20 10:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000f3', 'billing', 'fp-f3', 'true_positive', 'checkout latency', '2026-10-20 10:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000f4', NULL, 'fp-f4', 'true_positive', 'checkout', '2026-10-20 10:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000f5', 'billing', 'fp-f5', 'false_positive', 'batch export disk', '2026-10-20 10:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000f6', NULL, 'fp-f6', 'true_positive', 'tls certificate rotation', '2026-10-20 10:00:00+00');

INSERT INTO memory_embeddings (investigation_id, model, text_hash, dimensions, embedding, embedded_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000f1', 'test-model', 'x', 3, '[1,0.1,0]', now()),
  ('0192d3a4-0000-7000-8000-0000000000f2', 'test-model', 'x', 3, '[1,0.6,0]', now()),
  ('0192d3a4-0000-7000-8000-0000000000f3', 'test-model', 'x', 3, '[1,0,0]', now()),
  ('0192d3a4-0000-7000-8000-0000000000f4', 'test-model', 'x', 3, '[1,1.5,0]', now()),
  ('0192d3a4-0000-7000-8000-0000000000f5', 'test-model', 'x', 3, '[1,0.3,0]', now()),
  ('0192d3a4-0000-7000-8000-0000000000f6', 'old-model', 'x', 3, '[1,0,0]', now());
