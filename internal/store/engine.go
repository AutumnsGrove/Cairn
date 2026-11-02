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

// The session engine's transactions live here, beside the SQL, because the guarantees S07 makes are transactional:
// a version check and the change it guards, an event and the row it describes, a ledger entry and the effect it
// remembers. A caller that could run them separately could also break them.

// ErrStaleVersion means the change named a version that is not the current one. Nothing was written.
var ErrStaleVersion = errors.New("store: stale draft version")

// ErrInteractionResolved means the question was already answered. The first answer wins.
var ErrInteractionResolved = errors.New("store: interaction is already resolved")

// ErrDuplicateClientMessage means the ledger already holds this client_msg_id.
var ErrDuplicateClientMessage = errors.New("store: client message already recorded")

// Event kinds the engine writes. The set is closed so a typo cannot invent a kind a client will never handle.
const (
	EventUserMessage         = "user_message"
	EventPreread             = "preread"
	EventAssistantStart      = "assistant_start"
	EventTextDelta           = "text_delta"
	EventAssistantEnd        = "assistant_end"
	EventToolStart           = "tool_start"
	EventToolEnd             = "tool_end"
	EventInteractionRequest  = "interaction_request"
	EventInteractionResolved = "interaction_resolved"
	EventDraftSnapshot       = "draft_snapshot"
	EventSaved               = "saved"
	EventDiscarded           = "discarded"
	EventNotice              = "notice"
	EventError               = "error"
)

// Interaction is one question to open: its kind and the payload the client will draw.
type Interaction struct {
	SessionID string
	Kind      string
	Payload   json.RawMessage
}

// OpenInteraction writes the interaction_request event and the interaction row in one transaction, so a question
// can never be asked without being answerable and a restart can never lose one it asked.
func (s *Store) OpenInteraction(ctx context.Context, in Interaction) (domain.Interaction, domain.SessionEvent, error) {
	if in.Payload == nil {
		in.Payload = json.RawMessage("{}")
	}
	now := s.Now().UTC()
	id := s.NewID()
	var out domain.Interaction
	var event domain.SessionEvent
	payload, err := json.Marshal(map[string]any{
		"interaction_id": id,
		"kind":           in.Kind,
		"payload":        in.Payload,
	})
	if err != nil {
		return domain.Interaction{}, domain.SessionEvent{}, fmt.Errorf("store: encode interaction event: %w", err)
	}
	err = s.InTx(ctx, func(q *db.Queries) error {
		appended, appendErr := s.appendEvent(ctx, q, in.SessionID, EventInteractionRequest, payload)
		if appendErr != nil {
			return appendErr
		}
		event = appended
		if createErr := q.CreateInteraction(ctx, db.CreateInteractionParams{
			ID: id, SessionID: in.SessionID, Kind: in.Kind, PayloadJson: string(in.Payload),
			OpenedSeq: int64(appended.Seq), OpenedAt: now.Format(timeLayout),
		}); createErr != nil {
			return fmt.Errorf("store: create interaction: %w", createErr)
		}
		out = domain.Interaction{
			ID: id, SessionID: in.SessionID, Kind: in.Kind, Payload: in.Payload,
			OpenedSeq: appended.Seq, OpenedAt: now,
		}
		return nil
	})
	if err != nil {
		return domain.Interaction{}, domain.SessionEvent{}, err
	}
	return out, event, nil
}

// ResolveInteraction answers an open question, appends the resolution event and remembers the client message in one
// transaction. A second answer changes nothing and reports ErrInteractionResolved.
func (s *Store) ResolveInteraction(ctx context.Context, sessionID, interactionID string, by domain.ResolvedBy,
	resolution json.RawMessage, clientMsgID string,
) (domain.Interaction, domain.SessionEvent, error) {
	if resolution == nil {
		resolution = json.RawMessage("null")
	}
	var out domain.Interaction
	var event domain.SessionEvent
	err := s.InTx(ctx, func(q *db.Queries) error {
		row, err := q.GetInteraction(ctx, interactionID)
		if err != nil {
			return interactionErr(err)
		}
		if row.SessionID != sessionID {
			return ErrNotFound
		}
		if row.ResolvedSeq.Valid {
			return ErrInteractionResolved
		}
		payload, err := json.Marshal(map[string]any{
			"interaction_id": interactionID,
			"by":             string(by),
		})
		if err != nil {
			return fmt.Errorf("store: encode resolution event: %w", err)
		}
		appended, err := s.appendEvent(ctx, q, sessionID, EventInteractionResolved, payload)
		if err != nil {
			return err
		}
		event = appended
		res, err := q.ResolveInteraction(ctx, db.ResolveInteractionParams{
			ResolvedSeq:    sql.NullInt64{Int64: int64(appended.Seq), Valid: true},
			ResolvedBy:     sql.NullString{String: string(by), Valid: true},
			ResolutionJson: sql.NullString{String: string(resolution), Valid: true},
			ID:             interactionID, SessionID: sessionID,
		})
		if err != nil {
			return fmt.Errorf("store: resolve interaction: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrInteractionResolved
		}
		if clientMsgID != "" {
			if err := s.rememberClient(ctx, q, sessionID, clientMsgID, appended.Seq, "accepted", ""); err != nil {
				return err
			}
		}
		seq := appended.Seq
		out = domain.Interaction{
			ID: interactionID, SessionID: sessionID, Kind: row.Kind,
			Payload: json.RawMessage(row.PayloadJson), OpenedSeq: int(row.OpenedSeq),
			ResolvedSeq: &seq, ResolvedBy: by, Resolution: resolution,
		}
		return nil
	})
	if err != nil {
		return domain.Interaction{}, domain.SessionEvent{}, err
	}
	return out, event, nil
}

// Interaction reads one question.
func (s *Store) Interaction(ctx context.Context, id string) (domain.Interaction, error) {
	row, err := s.ReadQueries().GetInteraction(ctx, id)
	if err != nil {
		return domain.Interaction{}, interactionErr(err)
	}
	return interactionFromRow(row), nil
}

// OpenInteractions lists the questions still waiting on a session, oldest first.
func (s *Store) OpenInteractions(ctx context.Context, sessionID string) ([]domain.Interaction, error) {
	rows, err := s.ReadQueries().ListOpenInteractions(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: list open interactions: %w", err)
	}
	out := make([]domain.Interaction, 0, len(rows))
	for _, row := range rows {
		out = append(out, interactionFromRow(row))
	}
	return out, nil
}

// Interactions lists every question a session asked, for the record.
func (s *Store) Interactions(ctx context.Context, sessionID string) ([]domain.Interaction, error) {
	rows, err := s.ReadQueries().ListInteractions(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: list interactions: %w", err)
	}
	out := make([]domain.Interaction, 0, len(rows))
	for _, row := range rows {
		out = append(out, interactionFromRow(row))
	}
	return out, nil
}

// RememberClient records the first outcome of a client message. It is exported for the operations that are a single
// statement; the ones that change the draft record it inside their own transaction.
func (s *Store) RememberClient(ctx context.Context, m domain.ClientMessage) error {
	return s.InTx(ctx, func(q *db.Queries) error {
		return s.rememberClient(ctx, q, m.SessionID, m.ClientMsgID, m.Seq, m.Status, m.Reason)
	})
}

// rememberClient writes one ledger row or reports that the id is already known.
func (s *Store) rememberClient(ctx context.Context, q *db.Queries, sessionID, clientMsgID string, seq int,
	status, reason string,
) error {
	if clientMsgID == "" {
		return nil
	}
	if _, err := q.GetClientMessage(ctx, db.GetClientMessageParams{
		SessionID: sessionID, ClientMsgID: clientMsgID,
	}); err == nil {
		return ErrDuplicateClientMessage
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: read client message: %w", err)
	}
	if err := q.RememberClientMessage(ctx, db.RememberClientMessageParams{
		SessionID: sessionID, ClientMsgID: clientMsgID, Seq: int64(seq), Status: status,
		Reason: reason, CreatedAt: s.Now().UTC().Format(timeLayout),
	}); err != nil {
		return fmt.Errorf("store: remember client message: %w", err)
	}
	return nil
}

// ClientMessage reads the first outcome of a client message, or ErrNotFound.
func (s *Store) ClientMessage(ctx context.Context, sessionID, clientMsgID string) (domain.ClientMessage, error) {
	row, err := s.ReadQueries().GetClientMessage(ctx, db.GetClientMessageParams{
		SessionID: sessionID, ClientMsgID: clientMsgID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ClientMessage{}, ErrNotFound
	}
	if err != nil {
		return domain.ClientMessage{}, fmt.Errorf("store: read client message: %w", err)
	}
	created, _ := time.Parse(timeLayout, row.CreatedAt)
	return domain.ClientMessage{
		SessionID: row.SessionID, ClientMsgID: row.ClientMsgID, Seq: int(row.Seq),
		Status: row.Status, Reason: row.Reason, CreatedAt: created,
	}, nil
}

// LinkDraft attaches the entry a session is building.
func (s *Store) LinkDraft(ctx context.Context, sessionID, entryID string) error {
	return s.InTx(ctx, func(q *db.Queries) error {
		return q.LinkSessionEntry(ctx, db.LinkSessionEntryParams{
			EntryID: nullableString(entryID), UpdatedAt: s.Now().UTC().Format(timeLayout), ID: sessionID,
		})
	})
}

// SetMessages stores the conversation itself, which outlives the event log (docs/PROTOCOL.md 6).
func (s *Store) SetMessages(ctx context.Context, sessionID string, messages json.RawMessage) error {
	if len(messages) == 0 {
		messages = json.RawMessage("[]")
	}
	return s.InTx(ctx, func(q *db.Queries) error {
		return q.UpdateSessionMessages(ctx, db.UpdateSessionMessagesParams{
			MessagesJson: string(messages), UpdatedAt: s.Now().UTC().Format(timeLayout), ID: sessionID,
		})
	})
}

// BumpDraftVersion increments the card's version if the caller named the current one, and reports ErrStaleVersion
// when it did not. Every draft change goes through this, in the same transaction as the change.
func (s *Store) BumpDraftVersion(ctx context.Context, sessionID string, base int) (int, error) {
	var next int
	err := s.InTx(ctx, func(q *db.Queries) error {
		res, err := q.BumpDraftVersion(ctx, db.BumpDraftVersionParams{
			UpdatedAt: s.Now().UTC().Format(timeLayout), ID: sessionID, DraftVersion: int64(base),
		})
		if err != nil {
			return fmt.Errorf("store: bump draft version: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrStaleVersion
		}
		next = base + 1
		return nil
	})
	return next, err
}

// appendEvent is AppendEvent's body without the transaction, so a compound operation appends inside its own.
func (s *Store) appendEvent(ctx context.Context, q *db.Queries, sessionID, kind string, payload json.RawMessage) (domain.SessionEvent, error) {
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	now := s.Now().UTC()
	head, err := q.HeadSequence(ctx, sessionID)
	if err != nil {
		return domain.SessionEvent{}, fmt.Errorf("store: head sequence: %w", err)
	}
	id := s.NewID()
	if err := q.AppendSessionEvent(ctx, db.AppendSessionEventParams{
		ID: id, SessionID: sessionID, SessionID_2: sessionID, Kind: kind,
		PayloadJson: string(payload), CreatedAt: now.Format(timeLayout),
	}); err != nil {
		return domain.SessionEvent{}, fmt.Errorf("store: append event: %w", err)
	}
	return domain.SessionEvent{
		ID: id, SessionID: sessionID, Seq: int(asInt(head)) + 1, Kind: kind, Payload: payload, CreatedAt: now,
	}, nil
}

// interactionErr keeps "no such question" distinct from "already answered".
func interactionErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("store: read interaction: %w", err)
}

// interactionFromRow maps the stored question.
func interactionFromRow(row db.Interaction) domain.Interaction {
	out := domain.Interaction{
		ID: row.ID, SessionID: row.SessionID, Kind: row.Kind,
		Payload: json.RawMessage(row.PayloadJson), OpenedSeq: int(row.OpenedSeq),
	}
	if opened, err := time.Parse(timeLayout, row.OpenedAt); err == nil {
		out.OpenedAt = opened
	}
	if row.ResolvedSeq.Valid {
		seq := int(row.ResolvedSeq.Int64)
		out.ResolvedSeq = &seq
	}
	if row.ResolvedBy.Valid {
		out.ResolvedBy = domain.ResolvedBy(row.ResolvedBy.String)
	}
	if row.ResolutionJson.Valid {
		out.Resolution = json.RawMessage(row.ResolutionJson.String)
	}
	return out
}
