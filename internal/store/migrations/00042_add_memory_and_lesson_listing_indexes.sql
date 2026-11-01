-- Indexes for the lists of the web UI's Lessons and Memory page. The lists
-- page newest first and filter by several fields at once, and a page must
-- read about one page of rows however deep it is and however large the
-- table, so each filter has an index that returns rows in the order of the
-- page.
--
-- The memory list orders by (confirmed_at, investigation_id) descending and
-- continues after the last row of the page before with a comparison of that
-- pair, which an index on the pair in the same direction answers as a
-- range. The older confirmed_at index has no second column, so it cannot.
-- The verdict filter has no index of its own: it is applied to rows read in
-- order from the listing or service index.
--
-- The lessons list does the same with (created_at, id). Its existing
-- indexes all start with the status or cover only active lessons, so a list
-- of every status, of one service, or of words in the text could not use
-- them.

-- +goose Up
CREATE INDEX memory_entries_listing ON memory_entries (confirmed_at DESC, investigation_id DESC);
CREATE INDEX memory_entries_service_listing ON memory_entries (service, confirmed_at DESC, investigation_id DESC);
-- Only the disabled entries, which are a small part of the table.
CREATE INDEX memory_entries_disabled_listing ON memory_entries (confirmed_at DESC, investigation_id DESC) WHERE disabled;
-- The words filter of the list also finds disabled entries, which the
-- partial memory_entries_search leaves out. Entries are written rarely.
CREATE INDEX memory_entries_search_all ON memory_entries USING gin (search) WITH (fastupdate = off);

CREATE INDEX lessons_created ON lessons (created_at DESC, id DESC);
CREATE INDEX lessons_service_created ON lessons (scope_service, created_at DESC, id DESC);
-- The words filter across every status; lessons_search covers active ones.
CREATE INDEX lessons_search_all ON lessons USING gin (search);

-- +goose Down
DROP INDEX lessons_search_all;
DROP INDEX lessons_service_created;
DROP INDEX lessons_created;
DROP INDEX memory_entries_search_all;
DROP INDEX memory_entries_disabled_listing;
DROP INDEX memory_entries_service_listing;
DROP INDEX memory_entries_listing;
