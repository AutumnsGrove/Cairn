-- 0002_index_the_list_orders: make the two lists whose order was finished by a temporary sort use one index.
--
-- 0001's indexes stop at the first ORDER BY term, so SQLite searched by index and then sorted the ties in a
-- temporary B-tree. Adding the tiebreaker column to the index removes the sort, which is the difference between a
-- list that stays fast on a large library and one that does not (docs/slices/S04-data-layer.md 5).
--
-- Indexes are schema, not data: dropping and recreating them is safe, and the migration is append-only like every
-- other.

DROP INDEX entries_drafts;
CREATE INDEX entries_drafts ON entries (status, updated_at DESC, id DESC) WHERE status = 'draft';

DROP INDEX entries_work;
CREATE INDEX entries_work ON entries (work_id, finished_on DESC, id DESC);

-- Finding works by (medium, title) then ordering by year needs the year in the index, or the sort is temporary.
DROP INDEX works_medium_title;
CREATE INDEX works_medium_title ON works (medium, title, year DESC, id);

-- The library already used its index for both terms, but the index is rebuilt here with the same shape spelled out,
-- so the next reader can see that the order is covered rather than inferring it.
DROP INDEX entries_library;
CREATE INDEX entries_library ON entries (status, deleted_at, finished_on DESC, id DESC);
