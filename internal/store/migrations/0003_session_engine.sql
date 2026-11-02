-- 0003_session_engine: the three things the session engine needs that 0001 did not have a home for.
--
--   1. sessions.draft_version, so a stale edit is rejected by the database rather than by a read-then-write race.
--   2. interactions, so an open question survives a restart instead of living in a socket.
--   3. client_messages, so a retried client_msg_id has one effect even after the process has moved on.
--
-- Append-only and positional like every migration (docs/slices/S04-data-layer.md 1).

-- The draft's version: incremented in the same transaction as every change to the card. A change that names an
-- older version updates no row, and the engine turns that into a rejected ack with a fresh snapshot.
ALTER TABLE sessions ADD COLUMN draft_version INTEGER NOT NULL DEFAULT 0;

-- One question the service asked and she has to answer. It is written when opened, resolved once, and never
-- deleted: the audit trail of what was asked is as valuable as the answer.
CREATE TABLE interactions (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL REFERENCES sessions (id) ON DELETE RESTRICT,
    kind            TEXT NOT NULL,
    payload_json    TEXT NOT NULL DEFAULT '{}',
    opened_seq      INTEGER NOT NULL CHECK (opened_seq >= 1),
    opened_at       TEXT NOT NULL,
    resolved_seq    INTEGER CHECK (resolved_seq IS NULL OR resolved_seq > opened_seq),
    resolved_by     TEXT CHECK (resolved_by IS NULL OR resolved_by IN ('user','server')),
    resolution_json TEXT,
    CHECK ((resolved_seq IS NULL) = (resolved_by IS NULL))
);

CREATE INDEX interactions_open ON interactions (session_id, resolved_seq);

-- The client_msg_id ledger. A repeat of the same id finds the row and returns the first outcome, so a message typed
-- in a tunnel is neither lost nor applied twice (docs/PROTOCOL.md 6).
CREATE TABLE client_messages (
    session_id    TEXT NOT NULL REFERENCES sessions (id) ON DELETE RESTRICT,
    client_msg_id TEXT NOT NULL,
    seq           INTEGER NOT NULL DEFAULT 0 CHECK (seq >= 0),
    status        TEXT NOT NULL CHECK (status IN ('accepted','duplicate','rejected')),
    reason        TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    PRIMARY KEY (session_id, client_msg_id)
);

-- An interaction is resolved once and never rewritten: the first answer wins, like the first confirm.
CREATE TRIGGER interactions_no_reopen
BEFORE UPDATE OF resolved_seq, resolved_by, resolution_json ON interactions
WHEN OLD.resolved_seq IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'interaction is already resolved');
END;

-- Nothing deletes an interaction. The row is what was asked, and a question must stay answerable in the record.
CREATE TRIGGER interactions_no_delete
BEFORE DELETE ON interactions
BEGIN
    SELECT RAISE(ABORT, 'interactions are never deleted');
END;
