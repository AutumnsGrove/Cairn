// Package session is the state machine at the centre of Cairn: sessions, drafts, interactions, the event log,
// resume and the confirm. It has no model and no network, so every guarantee in docs/PROTOCOL.md section 6 is
// proven here by driving plain method calls (docs/slices/S07-session-engine.md).
//
// The transactions live in internal/store, beside the SQL: the version check and the change it guards, a question
// and the event that announces it, a ledger row and the effect it remembers. This package holds the rules and the
// shapes the protocol sends.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/store"
)

// ErrClosed means the session is no longer active. A saved or discarded session takes no more messages.
var ErrClosed = errors.New("session: this session is closed")

// ErrNoDraft means the session has no entry to build yet: the work has not been picked.
var ErrNoDraft = errors.New("session: this session has no draft")

// MaxMessagesPerSession is the length limit from docs/STANDARDS.md 4a.
const MaxMessagesPerSession = 200

// Config wires the engine. A nil clock or id source is a programming error in a test, not a runtime state, so the
// defaults are the real ones.
type Config struct {
	Store         *store.Store
	Now           func() time.Time
	NewID         func() string
	ServerVersion string
}

// Engine is the state machine over one store.
type Engine struct {
	store   *store.Store
	now     func() time.Time
	newID   func() string
	version string
}

// New builds the engine.
func New(cfg Config) *Engine {
	e := &Engine{store: cfg.Store, now: cfg.Now, newID: cfg.NewID, version: cfg.ServerVersion}
	if e.now == nil {
		e.now = time.Now
	}
	if e.newID == nil {
		e.newID = func() string { return fmt.Sprintf("id-%d", time.Now().UnixNano()) }
	}
	return e
}

// CreateEntry starts an entry session: the empty conversation a review is built in.
func (e *Engine) CreateEntry(ctx context.Context) (domain.Session, error) {
	return e.create(ctx, domain.SessionEntry, "")
}

// CreateGeneral starts a general chat, with its first question as the title so past chats can be listed.
func (e *Engine) CreateGeneral(ctx context.Context, title string) (domain.Session, error) {
	return e.create(ctx, domain.SessionGeneral, cleanTitleString(title))
}

// create writes one active session.
func (e *Engine) create(ctx context.Context, kind domain.SessionKind, title string) (domain.Session, error) {
	sess := domain.Session{
		ID: e.newID(), Kind: kind, Title: title, State: domain.SessionActive,
		Messages: json.RawMessage("[]"),
	}
	if err := e.store.CreateSession(ctx, sess); err != nil {
		return domain.Session{}, err
	}
	return e.store.Session(ctx, sess.ID)
}

// Session reads one session.
func (e *Engine) Session(ctx context.Context, id string) (domain.Session, error) {
	return e.store.Session(ctx, id)
}

// Close ends a session. Saving and discarding both go through here, and a closed session takes no more messages.
func (e *Engine) Close(ctx context.Context, sessionID string, state domain.SessionState) error {
	if state != domain.SessionSaved && state != domain.SessionDiscarded {
		return fmt.Errorf("session: %q is not a closing state", state)
	}
	sess, err := e.store.Session(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.State != domain.SessionActive {
		return ErrClosed
	}
	return e.store.SetSessionState(ctx, sessionID, state)
}

// Draft is the entry card with the version a client must quote to change it.
type Draft struct {
	Version int            `json:"version"`
	Entry   domain.Entry   `json:"entry"`
	Open    []OpenQuestion `json:"open_interactions,omitempty"`
}

// OpenQuestion is one question the card is waiting on, small enough to send with the draft.
type OpenQuestion struct {
	ID   string `json:"interaction_id"`
	Kind string `json:"kind"`
}

// Hello is the first message on every connection (docs/PROTOCOL.md 3).
type Hello struct {
	Session       domain.Session
	Draft         *Draft
	Open          []domain.Interaction
	Events        []domain.SessionEvent
	HeadSeq       int
	FullState     bool
	ServerVersion string
}

// Greet builds hello: the state a client needs, plus the replay it asked for.
//
// If the log no longer holds a contiguous run after lastSeq, FullState is true and the client rebuilds from the
// snapshot instead of replaying. That happens when compaction removed a turn's streamed chunks, and a client that
// resumed across it would otherwise miss text it never saw.
func (e *Engine) Greet(ctx context.Context, sessionID string, lastSeq int) (Hello, error) {
	sess, err := e.store.Session(ctx, sessionID)
	if err != nil {
		return Hello{}, err
	}
	head, err := e.store.HeadSequence(ctx, sessionID)
	if err != nil {
		return Hello{}, err
	}
	if lastSeq < 0 || lastSeq > head {
		return Hello{}, fmt.Errorf("session: last_seq %d is outside the log (head %d)", lastSeq, head)
	}

	hello := Hello{Session: sess, HeadSeq: head, ServerVersion: e.version}
	if lastSeq > 0 {
		events, replayErr := e.store.EventsSince(ctx, sessionID, lastSeq)
		if replayErr != nil {
			return Hello{}, replayErr
		}
		// A gap means events after lastSeq are gone, so replay cannot rebuild the conversation.
		if len(events) > 0 && events[0].Seq > lastSeq+1 {
			hello.FullState = true
		}
		if !hello.FullState {
			hello.Events = events
		}
	}

	open, err := e.store.OpenInteractions(ctx, sessionID)
	if err != nil {
		return Hello{}, err
	}
	hello.Open = open

	if sess.EntryID != "" {
		draft, draftErr := e.Draft(ctx, sessionID)
		if draftErr != nil {
			return Hello{}, draftErr
		}
		hello.Draft = &draft
	}
	return hello, nil
}

// Draft reads the card: the entry, its version, and the questions still open on it.
func (e *Engine) Draft(ctx context.Context, sessionID string) (Draft, error) {
	sess, err := e.store.Session(ctx, sessionID)
	if err != nil {
		return Draft{}, err
	}
	if sess.EntryID == "" {
		return Draft{}, ErrNoDraft
	}
	entry, err := e.store.Entry(ctx, sess.EntryID)
	if err != nil {
		return Draft{}, err
	}
	open, err := e.store.OpenInteractions(ctx, sessionID)
	if err != nil {
		return Draft{}, err
	}
	out := Draft{Version: sess.DraftVersion, Entry: entry}
	for _, question := range open {
		out.Open = append(out.Open, OpenQuestion{ID: question.ID, Kind: question.Kind})
	}
	return out, nil
}

// Append writes one event and returns it with the sequence number it was given.
func (e *Engine) Append(ctx context.Context, sessionID, kind string, data any) (domain.SessionEvent, error) {
	payload, err := encode(data)
	if err != nil {
		return domain.SessionEvent{}, err
	}
	return e.store.AppendEvent(ctx, sessionID, kind, payload)
}

// Compact drops the streamed chunks of finished turns. The text they built lives in the session's stored messages
// and in the assistant_end that follows them, so nothing a client can ask for is lost
// (docs/PROTOCOL.md 6, "Log size").
func (e *Engine) Compact(ctx context.Context, sessionID string) error {
	head, err := e.store.HeadSequence(ctx, sessionID)
	if err != nil {
		return err
	}
	return e.store.CompactTextDeltas(ctx, sessionID, head+1)
}

// Ask opens a question. The event and the row are written together, so a restart cannot lose a question it asked.
func (e *Engine) Ask(ctx context.Context, sessionID, kind string, payload any) (domain.Interaction, error) {
	sess, err := e.store.Session(ctx, sessionID)
	if err != nil {
		return domain.Interaction{}, err
	}
	if sess.State != domain.SessionActive {
		return domain.Interaction{}, ErrClosed
	}
	body, err := encode(payload)
	if err != nil {
		return domain.Interaction{}, err
	}
	question, _, err := e.store.OpenInteraction(ctx, store.Interaction{
		SessionID: sessionID, Kind: kind, Payload: body,
	})
	return question, err
}

// Answer resolves a question. The first answer wins; a second is refused and changes nothing. The client message id
// is remembered in the same transaction, so a retried tap cannot answer twice.
func (e *Engine) Answer(ctx context.Context, sessionID, interactionID string, value any, clientMsgID string) (domain.Interaction, error) {
	body, err := encode(value)
	if err != nil {
		return domain.Interaction{}, err
	}
	question, _, err := e.store.ResolveInteraction(ctx, sessionID, interactionID, domain.ResolvedByUser, body, clientMsgID)
	return question, err
}

// Withdraw closes a question the service no longer needs answered, such as a lookup question whose work arrived by
// another route. It is recorded as resolved by the server, so the card can clear it on every device.
func (e *Engine) Withdraw(ctx context.Context, sessionID, interactionID string) (domain.Interaction, error) {
	question, _, err := e.store.ResolveInteraction(ctx, sessionID, interactionID, domain.ResolvedByServer, json.RawMessage("null"), "")
	return question, err
}

// Seen reports whether a client message was already applied. It returns the first outcome when it was.
func (e *Engine) Seen(ctx context.Context, sessionID, clientMsgID string) (domain.ClientMessage, bool, error) {
	msg, err := e.store.ClientMessage(ctx, sessionID, clientMsgID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.ClientMessage{}, false, nil
	}
	if err != nil {
		return domain.ClientMessage{}, false, err
	}
	return msg, true, nil
}

// WaitingReason is why a draft is waiting on her. The vocabulary is fixed so the Drafts list can use plain words
// and the UI can map each to one sentence (S07 decisions 1).
type WaitingReason string

const (
	// WaitingPickWork means no work has been chosen yet; nothing can be saved until one is.
	WaitingPickWork WaitingReason = "pick_work"
	// WaitingRating means the card has no rating, which the confirm needs.
	WaitingRating WaitingReason = "needs_rating"
	// WaitingBody means the card has no body of her own words, which the confirm needs.
	WaitingBody WaitingReason = "needs_body"
	// WaitingFields means the work carries an unverified field she has not accepted. It does not block a save; it
	// is the next thing to look at.
	WaitingFields WaitingReason = "accept_fields"
	// WaitingDate means the card has no finished date. It does not block a save either, but the card asks for one.
	WaitingDate WaitingReason = "needs_date"
	// WaitingReady means nothing is outstanding.
	WaitingReady WaitingReason = "ready"
)

// DraftSummary is one row of the Drafts list: which session, what it is about, and what it is waiting for.
type DraftSummary struct {
	Session   domain.Session
	WorkTitle string
	Reason    WaitingReason
}

// Drafts lists active entry sessions with the first thing each is waiting for, newest first. The order of the
// checks is the order she would do them in: pick the work, rate it, say something, accept what came from the web,
// date it, done.
func (e *Engine) Drafts(ctx context.Context, limit int) ([]DraftSummary, error) {
	sessions, err := e.store.ActiveSessions(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]DraftSummary, 0, len(sessions))
	for _, sess := range sessions {
		if sess.Kind != domain.SessionEntry {
			continue
		}
		summary := DraftSummary{Session: sess, Reason: WaitingPickWork}
		if sess.EntryID == "" {
			out = append(out, summary)
			continue
		}
		entry, err := e.store.Entry(ctx, sess.EntryID)
		if err != nil {
			return nil, err
		}
		unverified := false
		if entry.WorkID != "" {
			sources, err := e.store.Sources(ctx, entry.WorkID)
			if err != nil {
				return nil, err
			}
			for _, source := range sources {
				if source.Trust == domain.TrustUnverified {
					unverified = true
					break
				}
			}
		}
		summary.WorkTitle = entry.WorkID
		if work, err := e.store.Work(ctx, entry.WorkID); err == nil {
			summary.WorkTitle = work.Title
		}
		summary.Reason = waitingReason(entry, unverified)
		out = append(out, summary)
	}
	return out, nil
}

// waitingReason applies the order above. Only a rating and a body block a save (docs/ENTRY_CARD.md 2); the others
// are what the card is asking her to look at next.
func waitingReason(entry domain.Entry, unverified bool) WaitingReason {
	switch {
	case entry.Rating == 0:
		return WaitingRating
	case entry.Body == "":
		return WaitingBody
	case unverified:
		return WaitingFields
	case entry.FinishedOn == "":
		return WaitingDate
	default:
		return WaitingReady
	}
}

// encode turns an event payload into JSON, refusing a value that cannot be sent.
func encode(data any) (json.RawMessage, error) {
	switch v := data.(type) {
	case nil:
		return json.RawMessage("null"), nil
	case json.RawMessage:
		return v, nil
	}
	body, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("session: encode payload: %w", err)
	}
	return body, nil
}

// cleanTitleString bounds a title so a chat list cannot be filled with a pasted novel.
func cleanTitleString(title string) string {
	const max = 200
	title = strings.TrimSpace(title)
	if len(title) > max {
		title = title[:max]
	}
	return title
}

// Refusal is a rule that said no, with a stable code the UI maps to its own copy. It is the engine's counterpart of
// the domain's GuardError: the same shape, because the client treats both the same way.
type Refusal struct {
	Code    string
	Message string
}

// Error renders the refusal for a log; the code is what the client receives.
func (r *Refusal) Error() string { return r.Code + ": " + r.Message }

// The codes a draft operation can be refused with.
const (
	CodeUnknownField = "unknown_field"
	CodeInvalidValue = "invalid_value"
	CodeStaleVersion = "stale_version"
	CodeNotADraft    = "not_a_draft"
	CodeMessageLong  = "message_too_long"
	CodeSessionFull  = "session_full"
	// CodeAlreadyAccepted means the tap was already taken: the database refuses a second accept.
	CodeAlreadyAccepted = "already_accepted"
)

// MaxMessageLength is the user message limit from docs/STANDARDS.md 4a.
const MaxMessageLength = 8000

// Edit is one draft_edit: the version it was made against, the fields it sets, and the message it came from.
type Edit struct {
	BaseVersion int
	Set         map[string]json.RawMessage
	ClientMsgID string
}

// EditDraft applies a field patch. An unknown field or a value outside its range is refused by name, and a stale
// version changes nothing and returns the current draft so the client can redraw
// (docs/PROTOCOL.md 5, "draft_edit").
func (e *Engine) EditDraft(ctx context.Context, sessionID string, in Edit) (Draft, error) {
	sess, err := e.store.Session(ctx, sessionID)
	if err != nil {
		return Draft{}, err
	}
	if sess.State != domain.SessionActive {
		return Draft{}, ErrClosed
	}
	if sess.EntryID == "" {
		return Draft{}, ErrNoDraft
	}

	patch, err := parsePatch(in.Set)
	if err != nil {
		return Draft{}, err
	}
	_, _, _, err = e.store.UpdateDraft(ctx, store.UpdateDraft{
		SessionID: sessionID, BaseVersion: in.BaseVersion, EntryID: sess.EntryID, ClientMsgID: in.ClientMsgID,
		Mutate: func(entry *domain.Entry) { patch(entry) },
	})
	if err != nil {
		if errors.Is(err, store.ErrStaleVersion) {
			return Draft{}, &Refusal{Code: CodeStaleVersion, Message: "The card changed on another screen; it has been refreshed."}
		}
		return Draft{}, err
	}
	return e.Draft(ctx, sessionID)
}

// parsePatch validates the fields a patch may set and returns the mutation. Anything else is refused by name, so a
// typo in a client cannot silently do nothing.
func parsePatch(set map[string]json.RawMessage) (func(*domain.Entry), error) {
	mutators := make([]func(*domain.Entry), 0, len(set))
	for field, raw := range set {
		field := field
		raw := raw
		switch field {
		case "rating":
			var value int
			if err := json.Unmarshal(raw, &value); err != nil || value < 0 || value > domain.MaxRating {
				return nil, invalid(field)
			}
			mutators = append(mutators, func(entry *domain.Entry) { entry.Rating = value })
		case "body":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil || len(value) > domain.MaxBodyLength {
				return nil, invalid(field)
			}
			mutators = append(mutators, func(entry *domain.Entry) {
				entry.Body = value
				// Typed on the card is her own input, which is the one origin that needs no proof.
				entry.BodyOrigin = domain.BodyTyped
				entry.BodySpans = nil
			})
		case "verdict":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil || len(value) > domain.MaxVerdictLength {
				return nil, invalid(field)
			}
			mutators = append(mutators, func(entry *domain.Entry) { entry.Verdict = value })
		case "format":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil || len(value) > 32 {
				return nil, invalid(field)
			}
			mutators = append(mutators, func(entry *domain.Entry) { entry.Format = value })
		case "finished_on":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, invalid(field)
			}
			mutators = append(mutators, func(entry *domain.Entry) { entry.FinishedOn = value })
		case "favourite":
			var value bool
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, invalid(field)
			}
			mutators = append(mutators, func(entry *domain.Entry) { entry.Favourite = value })
		case "season":
			var value *int
			if err := json.Unmarshal(raw, &value); err != nil || (value != nil && *value < 1) {
				return nil, invalid(field)
			}
			mutators = append(mutators, func(entry *domain.Entry) { entry.Season = value })
		default:
			return nil, &Refusal{Code: CodeUnknownField, Message: "The card has no field called " + field + "."}
		}
	}
	return func(entry *domain.Entry) {
		for _, mutate := range mutators {
			mutate(entry)
		}
	}, nil
}

// invalid is the refusal for a value outside its range.
func invalid(field string) error {
	return &Refusal{Code: CodeInvalidValue, Message: "That is not a value the " + field + " field can take."}
}

// ConfirmRequest carries the version the client saw and her messages, which is what an extracted body has to be
// contained in. SourceText is empty for a typed or imported body.
type ConfirmRequest struct {
	BaseVersion int
	ClientMsgID string
	SourceText  string
}

// Confirm is the commit. It runs the domain's save guard and then saves the entry, its revision, the session link,
// the closed state, the events and the ledger row in one transaction. Two devices confirming at once produce one
// save: the second finds the session closed or the version stale.
func (e *Engine) Confirm(ctx context.Context, sessionID string, in ConfirmRequest) (domain.Entry, int, []domain.SessionEvent, error) {
	draft, err := e.Draft(ctx, sessionID)
	if err != nil {
		return domain.Entry{}, 0, nil, err
	}
	if guardErr := domain.ValidateForSave(draft.Entry, in.SourceText); guardErr != nil {
		return domain.Entry{}, 0, nil, guardErr
	}
	entry, version, events, err := e.store.ConfirmDraft(ctx, store.Confirm{
		SessionID: sessionID, BaseVersion: in.BaseVersion, Entry: draft.Entry, ClientMsgID: in.ClientMsgID,
	})
	if errors.Is(err, store.ErrStaleVersion) {
		return domain.Entry{}, 0, nil, &Refusal{Code: CodeStaleVersion, Message: "The card changed on another screen; it has been refreshed."}
	}
	return entry, version, events, err
}

// Discard throws the draft away: the session closes, the draft stays in the database, and nothing is deleted.
func (e *Engine) Discard(ctx context.Context, sessionID string) error {
	sess, err := e.store.Session(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.State != domain.SessionActive {
		return ErrClosed
	}
	return e.store.DiscardDraft(ctx, sessionID)
}

// AppendUserMessage stores what she typed, enforcing the message and session limits, and records the ledger row.
func (e *Engine) AppendUserMessage(ctx context.Context, sessionID, text, clientMsgID string) (domain.SessionEvent, error) {
	if strings.TrimSpace(text) == "" {
		return domain.SessionEvent{}, &Refusal{Code: CodeInvalidValue, Message: "There is nothing in that message."}
	}
	if len(text) > MaxMessageLength {
		return domain.SessionEvent{}, &Refusal{Code: CodeMessageLong, Message: "That message is longer than Cairn accepts."}
	}
	sess, err := e.store.Session(ctx, sessionID)
	if err != nil {
		return domain.SessionEvent{}, err
	}
	if sess.State != domain.SessionActive {
		return domain.SessionEvent{}, ErrClosed
	}
	events, err := e.store.EventsSince(ctx, sessionID, 0)
	if err != nil {
		return domain.SessionEvent{}, err
	}
	messages := 0
	for _, event := range events {
		if event.Kind == store.EventUserMessage {
			messages++
		}
	}
	if messages >= MaxMessagesPerSession {
		return domain.SessionEvent{}, &Refusal{Code: CodeSessionFull, Message: "This conversation is full; start a new one."}
	}
	// The ledger is checked before anything is written, so a retried message is not stored twice.
	if _, seen, seenErr := e.Seen(ctx, sessionID, clientMsgID); seenErr != nil {
		return domain.SessionEvent{}, seenErr
	} else if seen {
		return domain.SessionEvent{}, store.ErrDuplicateClientMessage
	}
	event, err := e.Append(ctx, sessionID, store.EventUserMessage, map[string]string{
		"text": text, "message_id": e.newID(), "client_msg_id": clientMsgID,
	})
	if err != nil {
		return domain.SessionEvent{}, err
	}
	if ledgerErr := e.store.RememberClient(ctx, domain.ClientMessage{
		SessionID: sessionID, ClientMsgID: clientMsgID, Seq: event.Seq, Status: "accepted",
	}); ledgerErr != nil {
		return domain.SessionEvent{}, ledgerErr
	}
	return event, nil
}

// AcceptFields accepts the web-derived fields on the draft's work, which is the tap that makes a proposed fact
// canonical (docs/BUILD.md invariant 7). Every id must belong to that work and still be unverified; the database
// refuses anything else, and the version moves with the tap.
func (e *Engine) AcceptFields(ctx context.Context, sessionID string, baseVersion int, sourceIDs []string, clientMsgID string) (Draft, error) {
	sess, err := e.store.Session(ctx, sessionID)
	if err != nil {
		return Draft{}, err
	}
	if sess.State != domain.SessionActive {
		return Draft{}, ErrClosed
	}
	if sess.EntryID == "" {
		return Draft{}, ErrNoDraft
	}
	if len(sourceIDs) == 0 {
		return Draft{}, &Refusal{Code: CodeInvalidValue, Message: "There is nothing to accept."}
	}
	entry, err := e.store.Entry(ctx, sess.EntryID)
	if err != nil {
		return Draft{}, err
	}
	sources, err := e.store.Sources(ctx, entry.WorkID)
	if err != nil {
		return Draft{}, err
	}
	fields := make([]domain.Source, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		source, ok := sourceBelongsTo(sources, id)
		if !ok {
			return Draft{}, &Refusal{Code: CodeInvalidValue, Message: "That field is not on this work."}
		}
		fields = append(fields, source)
	}

	_, _, _, err = e.store.AcceptDraftSources(ctx, store.AcceptSources{
		SessionID: sessionID, BaseVersion: baseVersion, EntryID: sess.EntryID, WorkID: entry.WorkID,
		Fields: fields, ClientMsgID: clientMsgID,
	})
	if err != nil {
		if errors.Is(err, store.ErrStaleVersion) {
			return Draft{}, &Refusal{Code: CodeStaleVersion, Message: "The card changed on another screen; it has been refreshed."}
		}
		if errors.Is(err, store.ErrNotAcceptable) {
			return Draft{}, &Refusal{Code: CodeAlreadyAccepted, Message: "That field has already been accepted."}
		}
		return Draft{}, err
	}
	return e.Draft(ctx, sessionID)
}

// sourceBelongsTo finds the source a client named by "provider|id".
func sourceBelongsTo(sources []domain.Source, id string) (domain.Source, bool) {
	for _, source := range sources {
		if source.Provider+"|"+source.ProviderID == id {
			return source, true
		}
	}
	return domain.Source{}, false
}
