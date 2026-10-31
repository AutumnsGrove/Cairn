-- 0001_init: every table in PLAN.md 6, its indexes, and the triggers that make the rules true in the database.
--
-- The triggers are the point of this file. "Nothing is hard-deleted" and "history is append-only" are enforced
-- here, so a bug in a repository, a stray shell command or a future migration cannot break them silently
-- (docs/BUILD.md invariants 4, 12).

-- The work itself, canonical and merged.
CREATE TABLE works (
    id               TEXT PRIMARY KEY,
    medium           TEXT NOT NULL CHECK (medium IN ('film','tv','book','game','album','podcast','theatre','other')),
    title            TEXT NOT NULL,
    original_title   TEXT NOT NULL DEFAULT '',
    year             INTEGER NOT NULL DEFAULT 0 CHECK (year BETWEEN 0 AND 9999),
    creator_json     TEXT NOT NULL DEFAULT '[]',
    details_json     TEXT NOT NULL DEFAULT '{}',
    cover_path       TEXT NOT NULL DEFAULT '',
    cover_source     TEXT NOT NULL DEFAULT '',
    primary_provider TEXT NOT NULL DEFAULT '',
    primary_id       TEXT NOT NULL DEFAULT '',
    verified         INTEGER NOT NULL DEFAULT 0 CHECK (verified IN (0, 1)),
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE INDEX works_medium_title ON works (medium, title);
CREATE INDEX works_title ON works (title);

-- One row per source that contributed: history, not the source of truth.
CREATE TABLE work_sources (
    id                TEXT PRIMARY KEY,
    work_id           TEXT NOT NULL REFERENCES works (id) ON DELETE RESTRICT,
    provider          TEXT NOT NULL,
    provider_id       TEXT NOT NULL DEFAULT '',
    raw_json          TEXT NOT NULL DEFAULT '{}',
    trust             TEXT NOT NULL CHECK (trust IN ('verified','unverified','accepted')),
    citations_json    TEXT NOT NULL DEFAULT '[]',
    polaris_thread_id TEXT NOT NULL DEFAULT '',
    fetched_at        TEXT NOT NULL,
    accepted_at       TEXT
);

CREATE INDEX work_sources_work ON work_sources (work_id);

-- The operator's review of a work. One work can have several over time.
CREATE TABLE entries (
    id              TEXT PRIMARY KEY,
    work_id         TEXT NOT NULL REFERENCES works (id) ON DELETE RESTRICT,
    rating_q        INTEGER NOT NULL CHECK (rating_q BETWEEN 1 AND 20),
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

CREATE INDEX entries_library ON entries (status, deleted_at, finished_on DESC, id DESC);
CREATE INDEX entries_work ON entries (work_id, finished_on DESC);
CREATE INDEX entries_drafts ON entries (status, updated_at DESC) WHERE status = 'draft';

-- The blanks the agent asked about. Descriptor chips are answer_type 'tags' with a list of stable values.
CREATE TABLE answers (
    id          TEXT PRIMARY KEY,
    entry_id    TEXT NOT NULL REFERENCES entries (id) ON DELETE RESTRICT,
    question_id TEXT NOT NULL,
    category    TEXT NOT NULL DEFAULT '',
    prompt      TEXT NOT NULL,
    answer_type TEXT NOT NULL CHECK (answer_type IN ('tags','choice','short_text')),
    answer_json TEXT NOT NULL DEFAULT '[]',
    skipped     INTEGER NOT NULL DEFAULT 0 CHECK (skipped IN (0, 1)),
    created_at  TEXT NOT NULL
);

CREATE INDEX answers_entry ON answers (entry_id);

-- The app's own conversations. Nova's sessions are not importable, so these are ours.
CREATE TABLE sessions (
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL CHECK (kind IN ('entry','general')),
    title         TEXT NOT NULL DEFAULT '',
    entry_id      TEXT REFERENCES entries (id) ON DELETE RESTRICT,
    messages_json TEXT NOT NULL DEFAULT '[]',
    state         TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','saved','discarded')),
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE INDEX sessions_active ON sessions (state, updated_at DESC);
CREATE INDEX sessions_kind ON sessions (kind, updated_at DESC);

-- The per-session event log a reconnecting client resumes from. seq is strictly increasing per session.
CREATE TABLE session_events (
    id           TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions (id) ON DELETE RESTRICT,
    seq          INTEGER NOT NULL CHECK (seq >= 1),
    kind         TEXT NOT NULL,
    payload_json TEXT NOT NULL DEFAULT '{}',
    created_at   TEXT NOT NULL,
    UNIQUE (session_id, seq)
);

CREATE INDEX session_events_resume ON session_events (session_id, seq);

-- Append-only: the previous version of an entry at each confirmed edit.
CREATE TABLE entry_revisions (
    id            TEXT PRIMARY KEY,
    entry_id      TEXT NOT NULL REFERENCES entries (id) ON DELETE RESTRICT,
    revision      INTEGER NOT NULL CHECK (revision >= 1),
    snapshot_json TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    UNIQUE (entry_id, revision)
);

CREATE INDEX entry_revisions_entry ON entry_revisions (entry_id, revision DESC);

-- Every decisions call, so thresholds can be tuned from real use.
CREATE TABLE decision_calls (
    id            TEXT PRIMARY KEY,
    session_id    TEXT REFERENCES sessions (id) ON DELETE RESTRICT,
    message_index INTEGER NOT NULL DEFAULT 0,
    backend       TEXT NOT NULL,
    model         TEXT NOT NULL,
    odds_json     TEXT NOT NULL DEFAULT '{}',
    fired_json    TEXT NOT NULL DEFAULT '[]',
    cost_usd      REAL NOT NULL DEFAULT 0,
    latency_ms    INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL
);

CREATE INDEX decision_calls_session ON decision_calls (session_id);

CREATE TABLE tags (
    id   TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE entry_tags (
    entry_id TEXT NOT NULL REFERENCES entries (id) ON DELETE RESTRICT,
    tag_id   TEXT NOT NULL REFERENCES tags (id) ON DELETE RESTRICT,
    source   TEXT NOT NULL CHECK (source IN ('genre','mine')),
    PRIMARY KEY (entry_id, tag_id)
);

CREATE INDEX entry_tags_tag ON entry_tags (tag_id);

-- Screenshots that an import came from (Phase 3), and photos (Phase 5).
CREATE TABLE media (
    id            TEXT PRIMARY KEY,
    entry_id      TEXT REFERENCES entries (id) ON DELETE RESTRICT,
    session_id    TEXT REFERENCES sessions (id) ON DELETE RESTRICT,
    path          TEXT NOT NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('source_screenshot','photo')),
    transcript_json TEXT NOT NULL DEFAULT '{}',
    created_at    TEXT NOT NULL
);

CREATE INDEX media_entry ON media (entry_id);

-- R2 backup receipts.
CREATE TABLE mirrors (
    id      TEXT PRIMARY KEY,
    ran_at  TEXT NOT NULL,
    target  TEXT NOT NULL,
    status  TEXT NOT NULL CHECK (status IN ('ok','failed','skipped')),
    bytes   INTEGER NOT NULL DEFAULT 0,
    detail  TEXT NOT NULL DEFAULT ''
);

-- --- the rules, in the database -------------------------------------------------------------------------
--
-- A repository bug cannot delete a review, rewrite a revision, or quietly edit what a provider said. Each trigger
-- aborts with a message that names the rule, so a failure in a shell is self-explaining.

CREATE TRIGGER entries_no_delete
BEFORE DELETE ON entries
BEGIN
    SELECT RAISE(ABORT, 'entries are never deleted; soft delete sets deleted_at (invariant 4)');
END;

CREATE TRIGGER entry_revisions_no_delete
BEFORE DELETE ON entry_revisions
BEGIN
    SELECT RAISE(ABORT, 'entry_revisions is append-only; there is no purge (invariant 4)');
END;

CREATE TRIGGER entry_revisions_no_update
BEFORE UPDATE ON entry_revisions
BEGIN
    SELECT RAISE(ABORT, 'entry_revisions is append-only; a revision is never changed (invariant 4)');
END;

CREATE TRIGGER work_sources_no_delete
BEFORE DELETE ON work_sources
BEGIN
    SELECT RAISE(ABORT, 'work_sources is the audit trail; rows are never deleted (invariant 4)');
END;

-- The one legitimate update: accepting a web or image field. The trust moves from unverified to accepted exactly
-- once, and accepted_at is stamped. Every other change, including a second accept, aborts.
CREATE TRIGGER work_sources_only_accept
BEFORE UPDATE ON work_sources
WHEN NOT (
    OLD.trust = 'unverified'
    AND NEW.trust = 'accepted'
    AND OLD.accepted_at IS NULL
    AND NEW.accepted_at IS NOT NULL
    AND OLD.id = NEW.id
    AND OLD.work_id = NEW.work_id
    AND OLD.provider = NEW.provider
    AND OLD.provider_id = NEW.provider_id
    AND OLD.raw_json = NEW.raw_json
    AND OLD.citations_json = NEW.citations_json
    AND OLD.polaris_thread_id = NEW.polaris_thread_id
    AND OLD.fetched_at = NEW.fetched_at
)
BEGIN
    SELECT RAISE(ABORT, 'a source snapshot may only be accepted, never otherwise changed (invariant 4)');
END;

CREATE TRIGGER answers_no_delete
BEFORE DELETE ON answers
BEGIN
    SELECT RAISE(ABORT, 'answers are never deleted; they are part of the review (invariant 4)');
END;

-- A session event is written once and never changed: the log is what a client is replayed.
CREATE TRIGGER session_events_no_update
BEFORE UPDATE ON session_events
BEGIN
    SELECT RAISE(ABORT, 'session_events is an append-only log (invariant 4)');
END;

-- A saved entry cannot go back to draft, and a deleted entry cannot be un-deleted by an update to status alone.
CREATE TRIGGER entries_status_forward_only
BEFORE UPDATE OF status ON entries
WHEN OLD.status = 'saved' AND NEW.status = 'draft'
BEGIN
    SELECT RAISE(ABORT, 'a saved entry never becomes a draft again');
END;
