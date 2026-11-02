-- pragma: foreign_keys=off
-- 0004_allow_unrated_drafts: a draft may have no rating yet.
--
-- 0001 made rating_q NOT NULL CHECK (BETWEEN 1 AND 20), which quietly assumed a draft is always rated. It is not:
-- the Drafts list exists to say "Needs a rating" (mockups/03-home-and-library.html), and ENTRY_CARD 2 requires a
-- rating only to confirm. 0 now means "not rated yet"; every real rating is still 1..20.
--
-- SQLite cannot widen a CHECK in place, so this is its documented table rebuild. It needs foreign key enforcement
-- off for this file only, which the runner grants and then pays for with foreign_key_check and integrity_check
-- before it commits (internal/store/migrate.go).
--
-- Columns, indexes and triggers are reproduced exactly, because a rebuild that loses one of them loses a rule the
-- database was enforcing (docs/BUILD.md invariant 4).

CREATE TABLE entries_new (
    id              TEXT PRIMARY KEY,
    work_id         TEXT NOT NULL REFERENCES works (id) ON DELETE RESTRICT,
    rating_q        INTEGER NOT NULL DEFAULT 0 CHECK (rating_q BETWEEN 0 AND 20),
    rating_scale    TEXT NOT NULL DEFAULT 'quarter_stars_20',
    favourite       INTEGER NOT NULL DEFAULT 0 CHECK (favourite IN (0, 1)),
    season          INTEGER CHECK (season IS NULL OR season >= 1),
    verdict         TEXT NOT NULL DEFAULT '',
    body            TEXT NOT NULL,
    body_origin     TEXT NOT NULL CHECK (body_origin IN ('extracted','typed','mixed','imported')),
    imported_from   TEXT NOT NULL DEFAULT '',
    body_spans_json TEXT NOT NULL DEFAULT '[]',
    format          TEXT NOT NULL DEFAULT '',
    finished_on     TEXT,
    status          TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','saved','published')),
    deleted_at      TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

INSERT INTO entries_new (id, work_id, rating_q, rating_scale, favourite, season, verdict, body, body_origin,
                         imported_from, body_spans_json, format, finished_on, status, deleted_at, created_at,
                         updated_at)
SELECT id, work_id, rating_q, rating_scale, favourite, season, verdict, body, body_origin, imported_from,
       body_spans_json, format, finished_on, status, deleted_at, created_at, updated_at
FROM entries;

DROP TABLE entries;
ALTER TABLE entries_new RENAME TO entries;

-- The three indexes 0001 and 0002 settled on, reproduced with the tiebreakers that keep the lists from sorting.
CREATE INDEX entries_library ON entries (status, deleted_at, finished_on DESC, id DESC);
CREATE INDEX entries_work ON entries (work_id, finished_on DESC, id DESC);
CREATE INDEX entries_drafts ON entries (status, updated_at DESC, id DESC) WHERE status = 'draft';

-- The two entries triggers, reproduced verbatim. A table rebuild drops its triggers with the old table.
CREATE TRIGGER entries_no_delete
BEFORE DELETE ON entries
BEGIN
    SELECT RAISE(ABORT, 'entries are never deleted; soft delete sets deleted_at (invariant 4)');
END;

CREATE TRIGGER entries_status_forward_only
BEFORE UPDATE OF status ON entries
WHEN OLD.status = 'saved' AND NEW.status = 'draft'
BEGIN
    SELECT RAISE(ABORT, 'a saved entry never becomes a draft again');
END;
