package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AutumnsGrove/sepia/internal/domain"
	"github.com/AutumnsGrove/sepia/internal/store/db"
)

// The repositories. Each method is a small typed operation over the generated queries; the rules that must not be
// broken live in the schema, so these are the honest, boring layer that reads and writes rows (docs/BUILD.md 4).

// ErrNotFound is returned when a row the caller named does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrNotAcceptable is returned when a source cannot be accepted: it is already accepted, or it was verified and needs
// no tap. The database refuses the change; this names it.
var ErrNotAcceptable = errors.New("store: source is not unverified")

const timeLayout = time.RFC3339

// --- works ---------------------------------------------------------------------------------------------

// SaveWork inserts or updates a work. A work has no history of its own: the sources are its history.
func (s *Store) SaveWork(ctx context.Context, w domain.Work) error {
	creators, err := json.Marshal(w.Creators)
	if err != nil {
		return fmt.Errorf("store: encode creators: %w", err)
	}
	details, err := json.Marshal(w.Details)
	if err != nil {
		return fmt.Errorf("store: encode details: %w", err)
	}
	now := s.Now().UTC().Format(timeLayout)

	return s.InTx(ctx, func(q *db.Queries) error {
		_, err := q.GetWork(ctx, w.ID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return q.CreateWork(ctx, db.CreateWorkParams{
				ID: w.ID, Medium: w.Medium, Title: w.Title, OriginalTitle: w.OriginalTitle,
				Year: int64(w.Year), CreatorJson: string(creators), DetailsJson: string(details),
				CoverPath: w.CoverPath, CoverSource: w.CoverSource,
				PrimaryProvider: w.PrimaryProvider, PrimaryID: w.PrimaryID,
				Verified: boolToInt(w.Verified), CreatedAt: now, UpdatedAt: now,
			})
		case err != nil:
			return fmt.Errorf("store: read work: %w", err)
		default:
			return q.UpdateWork(ctx, db.UpdateWorkParams{
				Title: w.Title, OriginalTitle: w.OriginalTitle, Year: int64(w.Year),
				CreatorJson: string(creators), DetailsJson: string(details),
				CoverPath: w.CoverPath, CoverSource: w.CoverSource,
				PrimaryProvider: w.PrimaryProvider, PrimaryID: w.PrimaryID,
				Verified: boolToInt(w.Verified), UpdatedAt: now, ID: w.ID,
			})
		}
	})
}

// Work reads one work.
func (s *Store) Work(ctx context.Context, id string) (domain.Work, error) {
	row, err := s.ReadQueries().GetWork(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Work{}, ErrNotFound
	}
	if err != nil {
		return domain.Work{}, fmt.Errorf("store: read work: %w", err)
	}
	return workFromRow(row), nil
}

// WorksByTitle finds works with the same normalised title in a medium, which is the last of the merge's matching
// rules (PLAN.md 7).
func (s *Store) WorksByTitle(ctx context.Context, medium, title string) ([]domain.Work, error) {
	rows, err := s.ReadQueries().FindWorksByTitle(ctx, db.FindWorksByTitleParams{Medium: medium, Title: title})
	if err != nil {
		return nil, fmt.Errorf("store: find works: %w", err)
	}
	out := make([]domain.Work, 0, len(rows))
	for _, row := range rows {
		out = append(out, workFromRow(row))
	}
	return out, nil
}

// AddSource records what a provider said, exactly as it said it. It is never updated, except by AcceptSource.
func (s *Store) AddSource(ctx context.Context, workID string, src domain.Source) (string, error) {
	raw := src.Raw
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	citations, err := json.Marshal(src.Citations)
	if err != nil {
		return "", fmt.Errorf("store: encode citations: %w", err)
	}
	id := s.NewID()
	err = s.InTx(ctx, func(q *db.Queries) error {
		return q.CreateWorkSource(ctx, db.CreateWorkSourceParams{
			ID: id, WorkID: workID, Provider: src.Provider, ProviderID: src.ProviderID,
			RawJson: string(raw), Trust: string(src.Trust), CitationsJson: string(citations),
			PolarisThreadID: src.PolarisThreadID, FetchedAt: s.Now().UTC().Format(timeLayout),
		})
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Sources lists every source that contributed to a work, oldest first.
func (s *Store) Sources(ctx context.Context, workID string) ([]domain.Source, error) {
	rows, err := s.ReadQueries().ListWorkSources(ctx, workID)
	if err != nil {
		return nil, fmt.Errorf("store: list sources: %w", err)
	}
	out := make([]domain.Source, 0, len(rows))
	for _, row := range rows {
		src := domain.Source{
			Provider: row.Provider, ProviderID: row.ProviderID, Trust: domain.Trust(row.Trust),
			PolarisThreadID: row.PolarisThreadID, Raw: json.RawMessage(row.RawJson),
		}
		_ = json.Unmarshal([]byte(row.CitationsJson), &src.Citations)
		out = append(out, src)
	}
	return out, nil
}

// AcceptSource is the operator's tap on a web or image field. The database allows this once and refuses everything
// else about a snapshot, so this method is deliberately the only writer.
func (s *Store) AcceptSource(ctx context.Context, sourceID string) error {
	now := s.Now().UTC().Format(timeLayout)
	return s.InTx(ctx, func(q *db.Queries) error {
		// The statement's own WHERE clause is the guard, so the count is what tells the caller the tap did
		// something. A no-op that reports success would look like an accepted field that is not accepted.
		res, err := q.AcceptWorkSource(ctx, db.AcceptWorkSourceParams{AcceptedAt: &now, ID: sourceID})
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: accept source: %w", err)
		}
		if n == 0 {
			return ErrNotAcceptable
		}
		return nil
	})
}

// --- entries -------------------------------------------------------------------------------------------

// CreateDraft stores a new draft. A draft is an entry with status draft and never appears in the library.
func (s *Store) CreateDraft(ctx context.Context, e domain.Entry) error {
	params, err := s.entryParams(e)
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(q *db.Queries) error { return q.CreateEntry(ctx, params) })
}

// SaveEntry is the confirm tap: it saves the draft and writes the revision of what it replaced, in one transaction,
// so an edit can never exist without the version it replaced (docs/ENTRY_CARD.md 7).
func (s *Store) SaveEntry(ctx context.Context, e domain.Entry) (int, error) {
	now := s.Now().UTC()
	var revision int

	err := s.InTx(ctx, func(q *db.Queries) error {
		previous, err := q.GetEntry(ctx, e.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: read entry before saving: %w", err)
		}
		if err == nil && domain.Status(previous.Status) == domain.StatusSaved {
			// The revision is the entry as it was, written before the change, in this same transaction. A draft
			// is not a version: the first confirm has nothing to revise.
			number, revErr := q.LatestRevisionNumber(ctx, e.ID)
			if revErr != nil {
				return fmt.Errorf("store: latest revision: %w", revErr)
			}
			snapshot, snapErr := json.Marshal(entryFromRow(previous))
			if snapErr != nil {
				return fmt.Errorf("store: encode revision: %w", snapErr)
			}
			revision = int(asInt(number)) + 1
			if createErr := q.CreateRevision(ctx, db.CreateRevisionParams{
				ID: s.NewID(), EntryID: e.ID, Revision: int64(revision),
				SnapshotJson: string(snapshot), CreatedAt: now.Format(timeLayout),
			}); createErr != nil {
				return fmt.Errorf("store: write revision: %w", createErr)
			}
		}

		spans, err := json.Marshal(e.BodySpans)
		if err != nil {
			return fmt.Errorf("store: encode spans: %w", err)
		}
		return q.SaveEntry(ctx, db.SaveEntryParams{
			RatingQ: int64(e.Rating), Favourite: boolToInt(e.Favourite), Verdict: e.Verdict,
			Body: e.Body, BodyOrigin: string(e.BodyOrigin), BodySpansJson: string(spans),
			Format: e.Format, FinishedOn: nullableString(e.FinishedOn), UpdatedAt: now.Format(timeLayout),
			ID: e.ID,
		})
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}

// Entry reads one entry.
func (s *Store) Entry(ctx context.Context, id string) (domain.Entry, error) {
	row, err := s.ReadQueries().GetEntry(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Entry{}, ErrNotFound
	}
	if err != nil {
		return domain.Entry{}, fmt.Errorf("store: read entry: %w", err)
	}
	return entryFromRow(row), nil
}

// SoftDeleteEntry marks an entry deleted. There is no hard delete, and the database refuses one.
func (s *Store) SoftDeleteEntry(ctx context.Context, id string) error {
	now := s.Now().UTC().Format(timeLayout)
	return s.InTx(ctx, func(q *db.Queries) error {
		return q.SoftDeleteEntry(ctx, db.SoftDeleteEntryParams{DeletedAt: &now, UpdatedAt: now, ID: id})
	})
}

// RestoreEntry un-deletes a soft-deleted entry.
func (s *Store) RestoreEntry(ctx context.Context, id string) error {
	now := s.Now().UTC().Format(timeLayout)
	return s.InTx(ctx, func(q *db.Queries) error {
		return q.RestoreEntry(ctx, db.RestoreEntryParams{UpdatedAt: now, ID: id})
	})
}

// Library returns a page of saved entries, newest finished first, and the cursor for the next page. An empty next
// cursor means the list ended.
func (s *Store) Library(ctx context.Context, cursor domain.Cursor, limit int) ([]domain.Entry, domain.Cursor, error) {
	return s.LibraryPage(ctx, "", nil, cursor, limit)
}

// LibraryPage is the library with its two filters. medium is a provider's medium name and favourite is nil when the
// list is not filtered by it; both are applied in SQL so a filtered page is still one indexed read.
func (s *Store) LibraryPage(ctx context.Context, medium string, favourite *bool, cursor domain.Cursor,
	limit int,
) ([]domain.Entry, domain.Cursor, error) {
	limit = clampLimit(limit)
	var mediumArg, favouriteArg, beforeArg interface{}
	if medium != "" {
		mediumArg = medium
	}
	if favourite != nil {
		favouriteArg = boolToInt(*favourite)
	}
	if !cursor.Empty() {
		beforeArg = cursor.FinishedOn
	}
	rows, err := s.ReadQueries().ListLibraryPage(ctx, db.ListLibraryPageParams{
		Medium: mediumArg, Favourite: favouriteArg, BeforeFinishedOn: beforeArg,
		BeforeID: nullableString(cursor.ID), RowLimit: int64(limit + 1),
	})
	if err != nil {
		return nil, domain.Cursor{}, fmt.Errorf("store: list library: %w", err)
	}
	var next domain.Cursor
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = domain.Cursor{FinishedOn: derefString(last.FinishedOn), ID: last.ID}
	}
	out := make([]domain.Entry, 0, len(rows))
	for _, row := range rows {
		out = append(out, entryFromRow(row))
	}
	return out, next, nil
}

// Drafts returns drafts, most recently touched first, which is the Drafts list on the home screen.
func (s *Store) Drafts(ctx context.Context, limit int) ([]domain.Entry, error) {
	rows, err := s.ReadQueries().ListDrafts(ctx, int64(clampLimit(limit)))
	if err != nil {
		return nil, fmt.Errorf("store: list drafts: %w", err)
	}
	out := make([]domain.Entry, 0, len(rows))
	for _, row := range rows {
		out = append(out, entryFromRow(row))
	}
	return out, nil
}

// EntriesOnWork returns the timeline of entries on one work, newest first.
func (s *Store) EntriesOnWork(ctx context.Context, workID string) ([]domain.Entry, error) {
	rows, err := s.ReadQueries().ListEntriesOnWork(ctx, workID)
	if err != nil {
		return nil, fmt.Errorf("store: entries on work: %w", err)
	}
	out := make([]domain.Entry, 0, len(rows))
	for _, row := range rows {
		out = append(out, entryFromRow(row))
	}
	return out, nil
}

// Revisions returns an entry's history, newest first. It is append-only, so this is the whole truth.
func (s *Store) Revisions(ctx context.Context, entryID string) ([]domain.Revision, error) {
	all, _, err := s.RevisionsPage(ctx, entryID, 0, 0)
	if err != nil {
		return nil, err
	}
	return all, nil
}

// RevisionsPage returns one page of an entry's history, newest first, and a cursor for the next page. A zero
// before means the first page; a zero limit means every revision, which is what the library screen asks for.
func (s *Store) RevisionsPage(ctx context.Context, entryID string, before, limit int) ([]domain.Revision, int, error) {
	var beforeArg interface{}
	if before > 0 {
		beforeArg = int64(before)
	}
	rowLimit := int64(clampLimit(limit))
	if limit <= 0 {
		rowLimit = 1 << 31
	}
	rows, err := s.ReadQueries().ListRevisionsPage(ctx, db.ListRevisionsPageParams{
		EntryID: entryID, Before: beforeArg, RowLimit: rowLimit + 1,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("store: list revisions: %w", err)
	}
	var next int
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
		next = int(rows[len(rows)-1].Revision)
	}
	out := make([]domain.Revision, 0, len(rows))
	for _, row := range rows {
		var snapshot domain.Entry
		if err := json.Unmarshal([]byte(row.SnapshotJson), &snapshot); err != nil {
			return nil, 0, fmt.Errorf("store: decode revision: %w", err)
		}
		created, _ := time.Parse(timeLayout, row.CreatedAt)
		out = append(out, domain.Revision{
			ID: row.ID, EntryID: row.EntryID, Number: int(row.Revision), Snapshot: snapshot, CreatedAt: created,
		})
	}
	return out, next, nil
}

// EntryTags reads the genres and her own tags on an entry, in name order.
func (s *Store) EntryTags(ctx context.Context, entryID string) ([]domain.Tag, error) {
	rows, err := s.ReadQueries().ListEntryTags(ctx, entryID)
	if err != nil {
		return nil, fmt.Errorf("store: list entry tags: %w", err)
	}
	out := make([]domain.Tag, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.Tag{Name: row.Name, Source: row.Source})
	}
	return out, nil
}

// AddAnswers stores the answers for an entry, in the same transaction as the save that needs them.
func (s *Store) AddAnswers(ctx context.Context, entryID string, answers []domain.Answer) error {
	now := s.Now().UTC().Format(timeLayout)
	return s.InTx(ctx, func(q *db.Queries) error {
		for _, a := range answers {
			values, err := json.Marshal(a.Values)
			if err != nil {
				return fmt.Errorf("store: encode answer: %w", err)
			}
			id := a.ID
			if id == "" {
				id = s.NewID()
			}
			if err := q.CreateAnswer(ctx, db.CreateAnswerParams{
				ID: id, EntryID: entryID, QuestionID: a.QuestionID, Category: a.Category, Prompt: a.Prompt,
				AnswerType: string(a.Type), AnswerJson: string(values), Skipped: boolToInt(a.Skipped), CreatedAt: now,
			}); err != nil {
				return fmt.Errorf("store: create answer: %w", err)
			}
		}
		return nil
	})
}

// Answers returns an entry's answers in the order they were written.
func (s *Store) Answers(ctx context.Context, entryID string) ([]domain.Answer, error) {
	rows, err := s.ReadQueries().ListAnswers(ctx, entryID)
	if err != nil {
		return nil, fmt.Errorf("store: list answers: %w", err)
	}
	out := make([]domain.Answer, 0, len(rows))
	for _, row := range rows {
		var values []string
		if err := json.Unmarshal([]byte(row.AnswerJson), &values); err != nil {
			return nil, fmt.Errorf("store: decode answer: %w", err)
		}
		created, _ := time.Parse(timeLayout, row.CreatedAt)
		out = append(out, domain.Answer{
			ID: row.ID, EntryID: row.EntryID, QuestionID: row.QuestionID, Category: row.Category,
			Prompt: row.Prompt, Type: domain.AnswerType(row.AnswerType), Values: values,
			Skipped: row.Skipped == 1, CreatedAt: created,
		})
	}
	return out, nil
}

// --- sessions and the event log ------------------------------------------------------------------------

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, sess domain.Session) error {
	messages := sess.Messages
	if len(messages) == 0 {
		messages = json.RawMessage("[]")
	}
	now := s.Now().UTC().Format(timeLayout)
	return s.InTx(ctx, func(q *db.Queries) error {
		return q.CreateSession(ctx, db.CreateSessionParams{
			ID: sess.ID, Kind: string(sess.Kind), Title: sess.Title, EntryID: nullableString(sess.EntryID),
			MessagesJson: string(messages), State: string(sess.State), CreatedAt: now, UpdatedAt: now,
		})
	})
}

// Session reads one session.
func (s *Store) Session(ctx context.Context, id string) (domain.Session, error) {
	row, err := s.ReadQueries().GetSession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, ErrNotFound
	}
	if err != nil {
		return domain.Session{}, fmt.Errorf("store: read session: %w", err)
	}
	return sessionFromRow(row), nil
}

// SetSessionState moves a session to saved or discarded.
func (s *Store) SetSessionState(ctx context.Context, id string, state domain.SessionState) error {
	now := s.Now().UTC().Format(timeLayout)
	return s.InTx(ctx, func(q *db.Queries) error {
		return q.SetSessionState(ctx, db.SetSessionStateParams{State: string(state), UpdatedAt: now, ID: id})
	})
}

// ActiveSessions is the Drafts list of sessions that can be resumed.
func (s *Store) ActiveSessions(ctx context.Context, limit int) ([]domain.Session, error) {
	return s.Sessions(ctx, domain.SessionActive, "", "", limit)
}

// GeneralSessions is the past library chats, newest first.
func (s *Store) GeneralSessions(ctx context.Context, limit int) ([]domain.Session, error) {
	return s.Sessions(ctx, "", domain.SessionGeneral, "", limit)
}

// Sessions lists sessions newest first, with an optional state and kind. before is the opaque cursor from the
// previous page (see domain.SessionKey), empty on the first page.
func (s *Store) Sessions(ctx context.Context, state domain.SessionState, kind domain.SessionKind, before string, limit int) ([]domain.Session, error) {
	var stateArg, kindArg, beforeArg interface{}
	if state != "" {
		stateArg = string(state)
	}
	if kind != "" {
		kindArg = string(kind)
	}
	if before != "" {
		beforeArg = before
	}
	rows, err := s.ReadQueries().ListSessions(ctx, db.ListSessionsParams{
		State: stateArg, Kind: kindArg, Before: beforeArg, RowLimit: int64(clampLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	out := make([]domain.Session, 0, len(rows))
	for _, row := range rows {
		out = append(out, sessionFromRow(row))
	}
	return out, nil
}

// AppendEvent writes one event with the next sequence number for its session and returns it. The number is chosen by
// the statement and the unique index is the backstop, so a resume cannot replay an event twice.
func (s *Store) AppendEvent(ctx context.Context, sessionID, kind string, payload []byte) (domain.SessionEvent, error) {
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	now := s.Now().UTC()
	id := s.NewID()
	var out domain.SessionEvent
	err := s.InTx(ctx, func(q *db.Queries) error {
		head, err := q.HeadSequence(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("store: head sequence: %w", err)
		}
		if err := q.AppendSessionEvent(ctx, db.AppendSessionEventParams{
			ID: id, SessionID: sessionID, SessionID_2: sessionID, Kind: kind,
			PayloadJson: string(payload), CreatedAt: now.Format(timeLayout),
		}); err != nil {
			return fmt.Errorf("store: append event: %w", err)
		}
		out = domain.SessionEvent{
			ID: id, SessionID: sessionID, Seq: int(asInt(head)) + 1, Kind: kind,
			Payload: json.RawMessage(payload), CreatedAt: now,
		}
		return nil
	})
	return out, err
}

// EventsSince replays the log after a client's last seen sequence number.
func (s *Store) EventsSince(ctx context.Context, sessionID string, seq int) ([]domain.SessionEvent, error) {
	rows, err := s.ReadQueries().ListSessionEventsSince(ctx, db.ListSessionEventsSinceParams{
		SessionID: sessionID, Seq: int64(seq),
	})
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	out := make([]domain.SessionEvent, 0, len(rows))
	for _, row := range rows {
		created, _ := time.Parse(timeLayout, row.CreatedAt)
		out = append(out, domain.SessionEvent{
			ID: row.ID, SessionID: row.SessionID, Seq: int(row.Seq), Kind: row.Kind,
			Payload: json.RawMessage(row.PayloadJson), CreatedAt: created,
		})
	}
	return out, nil
}

// HeadSequence is the highest sequence number in a session, which hello reports.
func (s *Store) HeadSequence(ctx context.Context, sessionID string) (int, error) {
	head, err := s.ReadQueries().HeadSequence(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("store: head sequence: %w", err)
	}
	return int(asInt(head)), nil
}

// CompactTextDeltas removes the streamed chunks a finished turn has already collapsed into the stored message. The
// log is for resume only, so this loses nothing a client can ask for.
func (s *Store) CompactTextDeltas(ctx context.Context, sessionID string, beforeSeq int) error {
	return s.InTx(ctx, func(q *db.Queries) error {
		return q.DeleteCompactedEvents(ctx, db.DeleteCompactedEventsParams{
			SessionID: sessionID, Seq: int64(beforeSeq),
		})
	})
}

// --- helpers -------------------------------------------------------------------------------------------

func (s *Store) entryParams(e domain.Entry) (db.CreateEntryParams, error) {
	spans, err := json.Marshal(e.BodySpans)
	if err != nil {
		return db.CreateEntryParams{}, fmt.Errorf("store: encode spans: %w", err)
	}
	status := string(e.Status)
	if status == "" {
		status = string(domain.StatusDraft)
	}
	now := s.Now().UTC().Format(timeLayout)
	return db.CreateEntryParams{
		ID: e.ID, WorkID: e.WorkID, RatingQ: int64(e.Rating), RatingScale: domain.RatingScale,
		Favourite: boolToInt(e.Favourite), Season: nullableInt(e.Season), Verdict: e.Verdict, Body: e.Body,
		BodyOrigin: string(e.BodyOrigin), ImportedFrom: e.ImportedFrom, BodySpansJson: string(spans),
		Format: e.Format, FinishedOn: nullableString(e.FinishedOn), Status: status,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

func workFromRow(row db.Work) domain.Work {
	w := domain.Work{
		ID: row.ID, Medium: row.Medium, Title: row.Title, OriginalTitle: row.OriginalTitle, Year: int(row.Year),
		CoverPath: row.CoverPath, CoverSource: row.CoverSource,
		PrimaryProvider: row.PrimaryProvider, PrimaryID: row.PrimaryID, Verified: row.Verified == 1,
	}
	_ = json.Unmarshal([]byte(row.CreatorJson), &w.Creators)
	_ = json.Unmarshal([]byte(row.DetailsJson), &w.Details)
	return w
}

func entryFromRow(row db.Entry) domain.Entry {
	e := domain.Entry{
		ID: row.ID, WorkID: row.WorkID, Rating: int(row.RatingQ), RatingScale: row.RatingScale,
		Favourite: row.Favourite == 1, Season: intPtr(row.Season), Verdict: row.Verdict, Body: row.Body,
		BodyOrigin: domain.BodyOrigin(row.BodyOrigin), ImportedFrom: row.ImportedFrom,
		Format: row.Format, FinishedOn: derefString(row.FinishedOn), Status: domain.Status(row.Status),
		Deleted: row.DeletedAt != nil,
	}
	_ = json.Unmarshal([]byte(row.BodySpansJson), &e.BodySpans)
	e.CreatedAt, _ = time.Parse(timeLayout, row.CreatedAt)
	e.UpdatedAt, _ = time.Parse(timeLayout, row.UpdatedAt)
	return e
}

func sessionFromRow(row db.Session) domain.Session {
	out := domain.Session{
		ID: row.ID, Kind: domain.SessionKind(row.Kind), Title: row.Title,
		EntryID: derefString(row.EntryID), State: domain.SessionState(row.State),
		DraftVersion: int(row.DraftVersion),
		Messages:     json.RawMessage(row.MessagesJson),
	}
	out.CreatedAt, _ = time.Parse(timeLayout, row.CreatedAt)
	out.UpdatedAt, _ = time.Parse(timeLayout, row.UpdatedAt)
	return out
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullableInt(i *int) *int64 {
	if i == nil {
		return nil
	}
	v := int64(*i)
	return &v
}

func intPtr(i *int64) *int {
	if i == nil {
		return nil
	}
	v := int(*i)
	return &v
}

// asInt reads a value sqlc could not type, such as COALESCE(MAX(...)). SQLite returns integers as int64.
func asInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

// clampLimit keeps a page bounded whatever the caller asks for (docs/STANDARDS.md 4a: 50 default, 100 max).
func clampLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}
