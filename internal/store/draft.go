package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/store/db"
)

// The draft's writes are one transaction each, because every one of them changes the card's version, appends the
// snapshot a client resumes from, and remembers the client message that asked for it. Split apart, any two of them
// could disagree after a crash.

// UpdateDraft changes the card's fields under its version and returns the new entry, version and snapshot event.
// The mutate function is where the engine's field rules have already been applied; this layer only stores the
// result, so a rule cannot be bypassed by a second caller.
type UpdateDraft struct {
	SessionID   string
	BaseVersion int
	EntryID     string
	Mutate      func(*domain.Entry)
	ClientMsgID string
}

// UpdateDraft applies one edit.
func (s *Store) UpdateDraft(ctx context.Context, u UpdateDraft) (domain.Entry, int, domain.SessionEvent, error) {
	now := s.Now().UTC()
	var entry domain.Entry
	var version int
	var event domain.SessionEvent

	err := s.InTx(ctx, func(q *db.Queries) error {
		// The version check is first and is the guard for everything after it: no row updated means no change.
		next, err := bumpDraftVersion(ctx, q, u.SessionID, u.BaseVersion, now)
		if err != nil {
			return err
		}
		version = next

		row, err := q.GetEntry(ctx, u.EntryID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("store: read draft: %w", err)
		}
		if domain.Status(row.Status) != domain.StatusDraft {
			return fmt.Errorf("store: entry %s is not a draft", u.EntryID)
		}
		entry = entryFromRow(row)
		u.Mutate(&entry)

		spans, err := json.Marshal(entry.BodySpans)
		if err != nil {
			return fmt.Errorf("store: encode spans: %w", err)
		}
		res, err := q.UpdateDraft(ctx, db.UpdateDraftParams{
			RatingQ: int64(entry.Rating), RatingScale: entry.RatingScale, Favourite: boolToInt(entry.Favourite),
			Season: seasonPtr(entry.Season), Verdict: entry.Verdict, Body: entry.Body,
			BodyOrigin: string(entry.BodyOrigin), BodySpansJson: string(spans), Format: entry.Format,
			FinishedOn: nullableString(entry.FinishedOn), UpdatedAt: now.Format(timeLayout), ID: u.EntryID,
		})
		if err != nil {
			return fmt.Errorf("store: update draft: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("store: entry %s is not a draft", u.EntryID)
		}

		payload, err := json.Marshal(map[string]any{"version": version, "draft": entry})
		if err != nil {
			return fmt.Errorf("store: encode snapshot: %w", err)
		}
		appended, err := s.appendEvent(ctx, q, u.SessionID, EventDraftSnapshot, payload)
		if err != nil {
			return err
		}
		event = appended
		return s.rememberClient(ctx, q, u.SessionID, u.ClientMsgID, appended.Seq, acceptedStatus, "")
	})
	return entry, version, event, err
}

// Confirm is the one commit: the version check, the entry, its revision, the session link, the closed state, the
// snapshot and saved events and the ledger row all land together or not at all.
type Confirm struct {
	SessionID   string
	BaseVersion int
	Entry       domain.Entry
	ClientMsgID string
	// Revises names the saved entry a revision draft is editing. The working copy is written onto it, and the copy
	// itself is soft-deleted, so the library never gains a duplicate of a work she already reviewed.
	Revises string
}

// ConfirmDraft saves the entry. The domain guard has already run; this layer makes the save atomic.
func (s *Store) ConfirmDraft(ctx context.Context, c Confirm) (domain.Entry, int, []domain.SessionEvent, error) {
	now := s.Now().UTC()
	var entry domain.Entry
	var version int
	var events []domain.SessionEvent

	err := s.InTx(ctx, func(q *db.Queries) error {
		next, err := bumpDraftVersion(ctx, q, c.SessionID, c.BaseVersion, now)
		if err != nil {
			return err
		}
		version = next
		entry = c.Entry
		entry.Status = domain.StatusSaved

		target := entry.ID
		if c.Revises != "" {
			target = c.Revises
		}
		// An edit of a saved entry writes the version it replaced first, in this transaction, exactly as SaveEntry
		// does. A first confirm has nothing to revise.
		previous, err := q.GetEntry(ctx, target)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: read entry before confirming: %w", err)
		}
		if err == nil && domain.Status(previous.Status) == domain.StatusSaved {
			number, revErr := q.LatestRevisionNumber(ctx, target)
			if revErr != nil {
				return fmt.Errorf("store: latest revision: %w", revErr)
			}
			snapshot, snapErr := json.Marshal(entryFromRow(previous))
			if snapErr != nil {
				return fmt.Errorf("store: encode revision: %w", snapErr)
			}
			if createErr := q.CreateRevision(ctx, db.CreateRevisionParams{
				ID: s.NewID(), EntryID: target, Revision: asInt(number) + 1,
				SnapshotJson: string(snapshot), CreatedAt: now.Format(timeLayout),
			}); createErr != nil {
				return fmt.Errorf("store: write revision: %w", createErr)
			}
		}

		spans, err := json.Marshal(entry.BodySpans)
		if err != nil {
			return fmt.Errorf("store: encode spans: %w", err)
		}
		if err := q.SaveEntry(ctx, db.SaveEntryParams{
			RatingQ: int64(entry.Rating), Favourite: boolToInt(entry.Favourite), Verdict: entry.Verdict,
			Body: entry.Body, BodyOrigin: string(entry.BodyOrigin), BodySpansJson: string(spans),
			Format: entry.Format, FinishedOn: nullableString(entry.FinishedOn), UpdatedAt: now.Format(timeLayout),
			ID: target,
		}); err != nil {
			return fmt.Errorf("store: save entry: %w", err)
		}
		if target != entry.ID {
			// The working copy has served its purpose. It is soft-deleted, never removed: the row is the record of
			// what was edited before it became the revision.
			if err := q.SoftDeleteEntry(ctx, db.SoftDeleteEntryParams{
				DeletedAt: nullableString(now.Format(timeLayout)),
				UpdatedAt: now.Format(timeLayout), ID: entry.ID,
			}); err != nil {
				return fmt.Errorf("store: retire the working copy: %w", err)
			}
			entry.ID = target
		}
		if err := q.LinkSessionEntry(ctx, db.LinkSessionEntryParams{
			EntryID: nullableString(entry.ID), UpdatedAt: now.Format(timeLayout), ID: c.SessionID,
		}); err != nil {
			return fmt.Errorf("store: link session: %w", err)
		}
		if err := q.SetSessionState(ctx, db.SetSessionStateParams{
			State: string(domain.SessionSaved), UpdatedAt: now.Format(timeLayout), ID: c.SessionID,
		}); err != nil {
			return fmt.Errorf("store: close session: %w", err)
		}

		for _, step := range []struct {
			kind    string
			payload any
		}{
			{EventDraftSnapshot, map[string]any{"version": version, "draft": entry}},
			{EventSaved, map[string]any{"entry_id": entry.ID, "version": version}},
		} {
			body, err := json.Marshal(step.payload)
			if err != nil {
				return fmt.Errorf("store: encode %s: %w", step.kind, err)
			}
			appended, err := s.appendEvent(ctx, q, c.SessionID, step.kind, body)
			if err != nil {
				return err
			}
			events = append(events, appended)
		}
		return s.rememberClient(ctx, q, c.SessionID, c.ClientMsgID, events[0].Seq, acceptedStatus, "")
	})
	return entry, version, events, err
}

// DiscardDraft appends the closing event and marks the session, in one transaction.
func (s *Store) DiscardDraft(ctx context.Context, sessionID string) error {
	now := s.Now().UTC()
	return s.InTx(ctx, func(q *db.Queries) error {
		if _, err := s.appendEvent(ctx, q, sessionID, EventDiscarded, json.RawMessage("{}")); err != nil {
			return err
		}
		return q.SetSessionState(ctx, db.SetSessionStateParams{
			State: string(domain.SessionDiscarded), UpdatedAt: now.Format(timeLayout), ID: sessionID,
		})
	})
}

// acceptedStatus is the ledger status of a change that was applied.
const acceptedStatus = "accepted"

// seasonPtr widens the domain's season pointer for the generated parameter.
func seasonPtr(season *int) *int64 {
	if season == nil {
		return nil
	}
	v := int64(*season)
	return &v
}

// bumpDraftVersion is the shared first step: the version moves only if the caller named the current one and the
// session is still active.
func bumpDraftVersion(ctx context.Context, q *db.Queries, sessionID string, base int, now time.Time) (int, error) {
	res, err := q.BumpDraftVersion(ctx, db.BumpDraftVersionParams{
		UpdatedAt: now.Format(timeLayout), ID: sessionID, DraftVersion: int64(base),
	})
	if err != nil {
		return 0, fmt.Errorf("store: bump draft version: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, ErrStaleVersion
	}
	return base + 1, nil
}

// AcceptSources accepts web-derived fields into the canonical work, under the card's version and in one
// transaction with the snapshot and the ledger. The database's own trigger only lets an unverified source become
// accepted once, so a second tap on the same field is refused by the schema, not by this code.
type AcceptSources struct {
	SessionID   string
	BaseVersion int
	EntryID     string
	WorkID      string
	// Fields are the provider and id pairs the client named, which is all it ever saw.
	Fields      []domain.Source
	ClientMsgID string
}

// AcceptDraftSources applies one acceptance tap.
func (s *Store) AcceptDraftSources(ctx context.Context, a AcceptSources) (domain.Entry, int, domain.SessionEvent, error) {
	now := s.Now().UTC()
	var entry domain.Entry
	var version int
	var event domain.SessionEvent

	err := s.InTx(ctx, func(q *db.Queries) error {
		next, err := bumpDraftVersion(ctx, q, a.SessionID, a.BaseVersion, now)
		if err != nil {
			return err
		}
		version = next

		accepted := now.Format(timeLayout)
		for _, field := range a.Fields {
			res, acceptErr := q.AcceptWorkSourceByProvider(ctx, db.AcceptWorkSourceByProviderParams{
				AcceptedAt: &accepted, WorkID: a.WorkID, Provider: field.Provider, ProviderID: field.ProviderID,
			})
			if acceptErr != nil {
				return fmt.Errorf("store: accept %s %s: %w", field.Provider, field.ProviderID, acceptErr)
			}
			if n, _ := res.RowsAffected(); n != 1 {
				// The trigger is the guard, so a second accept of the same field is a refusal rather than a
				// silent success: a tap that did nothing must not look like a tap that worked.
				return ErrNotAcceptable
			}
		}

		row, err := q.GetEntry(ctx, a.EntryID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("store: read draft: %w", err)
		}
		entry = entryFromRow(row)

		payload, err := json.Marshal(map[string]any{"version": version, "draft": entry})
		if err != nil {
			return fmt.Errorf("store: encode snapshot: %w", err)
		}
		appended, err := s.appendEvent(ctx, q, a.SessionID, EventDraftSnapshot, payload)
		if err != nil {
			return err
		}
		event = appended
		return s.rememberClient(ctx, q, a.SessionID, a.ClientMsgID, appended.Seq, acceptedStatus, "")
	})
	return entry, version, event, err
}
