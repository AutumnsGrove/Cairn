-- Every query the repositories run. sqlc generates typed Go from these, so the SQL is the source of truth and the
-- generated code is committed (docs/STANDARDS.md 2).

-- name: CreateWork :exec
INSERT INTO works (id, medium, title, original_title, year, creator_json, details_json, cover_path,
                   cover_source, primary_provider, primary_id, verified, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetWork :one
SELECT * FROM works WHERE id = ?;

-- name: UpdateWork :exec
UPDATE works
SET title = ?, original_title = ?, year = ?, creator_json = ?, details_json = ?, cover_path = ?,
    cover_source = ?, primary_provider = ?, primary_id = ?, verified = ?, updated_at = ?
WHERE id = ?;

-- name: FindWorksByTitle :many
SELECT * FROM works WHERE medium = ? AND title = ? ORDER BY year DESC, id;

-- name: CreateWorkSource :exec
INSERT INTO work_sources (id, work_id, provider, provider_id, raw_json, trust, citations_json,
                          polaris_thread_id, fetched_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListWorkSources :many
SELECT * FROM work_sources WHERE work_id = ? ORDER BY fetched_at, id;

-- name: AcceptWorkSource :exec
-- The one legitimate update to a snapshot: an unverified web field, accepted once. The trigger enforces the rest.
UPDATE work_sources SET trust = 'accepted', accepted_at = ? WHERE id = ? AND trust = 'unverified';

-- name: CreateEntry :exec
INSERT INTO entries (id, work_id, rating_q, rating_scale, favourite, season, verdict, body, body_origin,
                     imported_from, body_spans_json, format, finished_on, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetEntry :one
SELECT * FROM entries WHERE id = ?;

-- name: SaveEntry :exec
-- The confirm tap. Only a draft becomes saved, and only through this statement.
UPDATE entries
SET rating_q = ?, favourite = ?, verdict = ?, body = ?, body_origin = ?, body_spans_json = ?, format = ?,
    finished_on = ?, status = 'saved', updated_at = ?
WHERE id = ? AND status = 'draft';

-- name: SoftDeleteEntry :exec
UPDATE entries SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL;

-- name: RestoreEntry :exec
UPDATE entries SET deleted_at = NULL, updated_at = ? WHERE id = ?;

-- name: ListLibrary :many
-- The library: saved, not deleted, newest finished first. Cursor pagination on (finished_on, id), never an offset.
-- The four placeholders are: the cursor's finished_on (NULL on the first page), its id, and the limit. They are
-- positional because a repeated named argument makes sqlc emit fewer struct fields than the driver has "?".
-- A row-value comparison is used so SQLite can walk the (finished_on, id) index instead of sorting.
SELECT * FROM entries
WHERE status = 'saved' AND deleted_at IS NULL
  AND (? IS NULL OR (finished_on, id) < (?, ?))
ORDER BY finished_on DESC, id DESC
LIMIT ?;

-- name: ListDrafts :many
SELECT * FROM entries WHERE status = 'draft' AND deleted_at IS NULL ORDER BY updated_at DESC, id DESC LIMIT ?;

-- name: ListEntriesOnWork :many
SELECT * FROM entries WHERE work_id = ? AND deleted_at IS NULL ORDER BY finished_on DESC, id DESC;

-- name: CreateAnswer :exec
INSERT INTO answers (id, entry_id, question_id, category, prompt, answer_type, answer_json, skipped, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListAnswers :many
SELECT * FROM answers WHERE entry_id = ? ORDER BY created_at, id;

-- name: CreateRevision :exec
-- Written in the same transaction as the confirm that creates it.
INSERT INTO entry_revisions (id, entry_id, revision, snapshot_json, created_at) VALUES (?, ?, ?, ?, ?);

-- name: ListRevisions :many
SELECT * FROM entry_revisions WHERE entry_id = ? ORDER BY revision DESC;

-- name: LatestRevisionNumber :one
SELECT COALESCE(MAX(revision), 0) FROM entry_revisions WHERE entry_id = ?;

-- name: CreateSession :exec
INSERT INTO sessions (id, kind, title, entry_id, messages_json, state, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSession :one
SELECT * FROM sessions WHERE id = ?;

-- name: ListActiveSessions :many
SELECT * FROM sessions WHERE state = 'active' ORDER BY updated_at DESC, id DESC LIMIT ?;

-- name: ListGeneralSessions :many
SELECT * FROM sessions WHERE kind = 'general' ORDER BY updated_at DESC, id DESC LIMIT ?;

-- name: SetSessionState :exec
UPDATE sessions SET state = ?, updated_at = ? WHERE id = ?;

-- name: UpdateSessionMessages :exec
UPDATE sessions SET messages_json = ?, updated_at = ? WHERE id = ?;

-- name: AppendSessionEvent :exec
-- The sequence number is chosen in the same statement, so two appenders cannot pick the same one. The unique
-- index on (session_id, seq) is the backstop: if two ever did, one of them fails rather than being replayed twice.
INSERT INTO session_events (id, session_id, seq, kind, payload_json, created_at)
SELECT ?, ?, COALESCE((SELECT MAX(seq) FROM session_events WHERE session_events.session_id = ?), 0) + 1,
       ?, ?, ?;

-- name: ListSessionEventsSince :many
SELECT * FROM session_events WHERE session_id = ? AND seq > ? ORDER BY seq;

-- name: HeadSequence :one
SELECT COALESCE(MAX(seq), 0) FROM session_events WHERE session_id = ?;

-- name: DeleteCompactedEvents :exec
DELETE FROM session_events WHERE session_id = ? AND kind = 'text_delta' AND seq < ?;

-- name: CreateDecisionCall :exec
INSERT INTO decision_calls (id, session_id, message_index, backend, model, odds_json, fired_json, cost_usd,
                            latency_ms, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpsertTag :exec
INSERT INTO tags (id, name) VALUES (?, ?) ON CONFLICT (name) DO NOTHING;

-- name: GetTagByName :one
SELECT * FROM tags WHERE name = ?;

-- name: LinkEntryTag :exec
INSERT INTO entry_tags (entry_id, tag_id, source) VALUES (?, ?, ?) ON CONFLICT (entry_id, tag_id) DO NOTHING;

-- name: ListEntryTags :many
SELECT t.id, t.name, et.source FROM entry_tags et JOIN tags t ON t.id = et.tag_id
WHERE et.entry_id = ? ORDER BY t.name;

-- name: CreateMedia :exec
INSERT INTO media (id, entry_id, session_id, path, kind, transcript_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListMediaForEntry :many
SELECT * FROM media WHERE entry_id = ? ORDER BY created_at;

-- name: RecordMirror :exec
INSERT INTO mirrors (id, ran_at, target, status, bytes, detail) VALUES (?, ?, ?, ?, ?, ?);

-- name: ListMirrors :many
SELECT * FROM mirrors ORDER BY ran_at DESC LIMIT ?;
