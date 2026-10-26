-- Seed data for the storetest example tests. A feature package inserts rows
-- into the tables its migrations create. There are no feature tables yet,
-- so this example creates its own table first.
CREATE TABLE example_notes (
  id   uuid PRIMARY KEY,
  body text NOT NULL
);

INSERT INTO example_notes (id, body) VALUES
  ('019a2000-0000-7000-8000-000000000001', 'first note'),
  ('019a2000-0000-7000-8000-000000000002', 'second note');
