package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AutumnsGrove/sepia/internal/agent"
	"github.com/AutumnsGrove/sepia/internal/domain"
	"github.com/AutumnsGrove/sepia/internal/session"
)

// The wire shapes are the contract in api/events.schema.json and api/openapi.yaml, written out by hand because the
// stored records are richer (and sometimes different) than what a client is shown. Every field name here is the
// schema's; a contract test validates real output against both documents, so this file and the contract cannot
// drift (docs/slices/S09-server.md S09.4).

// wireEnvelope is one server event. seq is a pointer so a live-only event marshals as null rather than being
// omitted, which is the protocol's one way to tell "never replay this" (docs/PROTOCOL.md 2).
type wireEnvelope struct {
	Seq       *int   `json:"seq"`
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	TS        string `json:"ts"`
	Data      any    `json:"data"`
}

// instant is the one time format the API sends: RFC 3339 in UTC, never a display time.
func instant(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func encodeEnvelope(seq *int, kind, sessionID string, at time.Time, data any) ([]byte, error) {
	raw, err := json.Marshal(wireEnvelope{Seq: seq, Type: kind, SessionID: sessionID, TS: instant(at), Data: data})
	if err != nil {
		return nil, fmt.Errorf("server: encode %s: %w", kind, err)
	}
	return raw, nil
}

// --- the entry card --------------------------------------------------------------------------------------

type wireRating struct {
	Quarters  int    `json:"quarters"`
	Scale     string `json:"scale"`
	Favourite bool   `json:"favourite"`
}

type wireBlocks struct {
	Work    string `json:"work"`
	Rating  string `json:"rating"`
	Body    string `json:"body"`
	Facts   string `json:"facts"`
	Answers string `json:"answers"`
}

type wireCreator struct {
	Role string `json:"role"`
	Name string `json:"name"`
}

type wireWork struct {
	ID              string        `json:"id,omitempty"`
	Medium          string        `json:"medium,omitempty"`
	Title           string        `json:"title"`
	OriginalTitle   string        `json:"original_title,omitempty"`
	Year            int           `json:"year,omitempty"`
	Creators        []wireCreator `json:"creators,omitempty"`
	CoverURL        string        `json:"cover_url,omitempty"`
	Verified        bool          `json:"verified"`
	PrimaryProvider string        `json:"primary_provider,omitempty"`
	PrimaryID       string        `json:"primary_id,omitempty"`
}

type wireSegment struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type wireUnverified struct {
	ID        string `json:"id"`
	Field     string `json:"field"`
	Value     string `json:"value"`
	SourceURL string `json:"source_url,omitempty"`
	Snippet   string `json:"snippet,omitempty"`
	FromImage bool   `json:"from_image,omitempty"`
}

type wireTag struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

type wireFidelity struct {
	Status  string `json:"status"`
	Warning string `json:"warning,omitempty"`
}

type wireWhyCheck struct {
	Check       string  `json:"check"`
	Option      string  `json:"option"`
	Probability float64 `json:"probability"`
	Fired       bool    `json:"fired"`
}

type wireToolSource struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

type wireWhyTool struct {
	Tool    string           `json:"tool"`
	OK      bool             `json:"ok"`
	Summary string           `json:"summary,omitempty"`
	Sources []wireToolSource `json:"sources,omitempty"`
}

// wireCost is money on the wire: the total, the part a plan covered, and whether any of it is a price-list
// estimate rather than a provider's own report.
type wireCost struct {
	TotalUSD   float64 `json:"total_usd"`
	CoveredUSD float64 `json:"covered_usd"`
	Estimated  bool    `json:"estimated,omitempty"`
}

// wireCostOf shapes one summary. The two numbers are always sent, because a screen that shows only the total would
// be showing money she may not have spent.
func wireCostOf(cost domain.CostSummary) wireCost {
	return wireCost{TotalUSD: cost.Total, CoveredUSD: cost.Covered, Estimated: cost.Estimated}
}

type wireWhy struct {
	Checks      []wireWhyCheck `json:"checks,omitempty"`
	Tools       []wireWhyTool  `json:"tools,omitempty"`
	PrereadCost wireCost       `json:"preread_cost"`
	TurnCost    wireCost       `json:"turn_cost"`
}

type wireAnswer struct {
	QuestionID string   `json:"question_id"`
	Category   string   `json:"category,omitempty"`
	Prompt     string   `json:"prompt"`
	AnswerType string   `json:"answer_type"`
	Values     []string `json:"values"`
	Skipped    bool     `json:"skipped,omitempty"`
}

type wireCard struct {
	Version    int              `json:"version"`
	Work       wireWork         `json:"work"`
	Rating     wireRating       `json:"rating"`
	Verdict    string           `json:"verdict,omitempty"`
	Body       string           `json:"body"`
	BodyOrigin string           `json:"body_origin"`
	BodySpans  []domain.Span    `json:"body_spans,omitempty"`
	Format     string           `json:"format,omitempty"`
	FinishedOn string           `json:"finished_on,omitempty"`
	Blocks     wireBlocks       `json:"blocks"`
	LeftOut    []wireSegment    `json:"left_out,omitempty"`
	Answers    []wireAnswer     `json:"answers,omitempty"`
	Tags       []wireTag        `json:"tags,omitempty"`
	Unverified []wireUnverified `json:"unverified,omitempty"`
	Fidelity   *wireFidelity    `json:"fidelity,omitempty"`
	Why        *wireWhy         `json:"why,omitempty"`
	CanConfirm bool             `json:"can_confirm"`
	Missing    []string         `json:"missing"`
}

// wireCardOf turns the engine's card into the contract's card. Everything the client can see is decided here, so
// a change to what a card means is one function and its contract test.
func (s *Server) wireCardOf(card session.Card) wireCard {
	entry := card.Draft.Entry
	out := wireCard{
		Version:    card.Draft.Version,
		Work:       wireWorkOf(card.Work),
		Rating:     wireRating{Quarters: entry.Rating, Scale: domain.RatingScale, Favourite: entry.Favourite},
		Verdict:    entry.Verdict,
		Body:       entry.Body,
		BodyOrigin: string(entry.BodyOrigin),
		Format:     entry.Format,
		FinishedOn: entry.FinishedOn,
		Blocks:     wireBlocksOf(entry, card.Work, card.Answers, card.Sources),
	}
	if len(entry.BodySpans) > 0 {
		out.BodySpans = entry.BodySpans
	}
	for _, segment := range card.LeftOut {
		out.LeftOut = append(out.LeftOut, wireSegment{
			ID: segmentID(segment), Text: clipRunes(segment.Text, 8000),
		})
	}
	for _, answer := range card.Answers {
		out.Answers = append(out.Answers, wireAnswer{
			QuestionID: answer.QuestionID, Category: answer.Category, Prompt: answer.Prompt,
			AnswerType: string(answer.Type), Values: answer.Values, Skipped: answer.Skipped,
		})
	}
	for _, tag := range card.Tags {
		out.Tags = append(out.Tags, wireTag{Name: clipRunes(tag.Name, 100), Source: tag.Source})
	}
	out.Unverified = unverifiedOf(card.Sources)
	out.Fidelity = fidelityOfCard(card.Fidelity)
	out.Why = whyOf(card)
	out.CanConfirm, out.Missing = domain.CanConfirm(entry, card.SourceText)
	if out.Missing == nil {
		out.Missing = []string{}
	}
	return out
}

func wireWorkOf(work domain.Work) wireWork {
	out := wireWork{
		ID: work.ID, Medium: wireMedium(work.Medium), Title: work.Title, OriginalTitle: work.OriginalTitle,
		Year: work.Year, Verified: work.Verified, PrimaryProvider: work.PrimaryProvider, PrimaryID: work.PrimaryID,
	}
	// The card has to name something even when the work row is thin, because title is the one required field.
	if strings.TrimSpace(out.Title) == "" {
		out.Title = work.ID
	}
	if out.Title == "" {
		out.Title = "Untitled"
	}
	for _, creator := range work.Creators {
		out.Creators = append(out.Creators, wireCreator{Role: creator.Role, Name: clipRunes(creator.Name, 200)})
	}
	if work.CoverPath != "" {
		out.CoverURL = "/covers/" + strings.TrimPrefix(strings.TrimPrefix(work.CoverPath, "/"), "covers/")
	}
	return out
}

// wireMedium keeps the medium inside the contract's closed set. A provider that sends something else is shown as
// "other" rather than breaking the card.
func wireMedium(medium string) string {
	switch medium {
	case "film", "tv", "book", "game", "album", "podcast", "theatre", "other":
		return medium
	case "":
		return ""
	default:
		return "other"
	}
}

func wireBlocksOf(entry domain.Entry, work domain.Work, answers []domain.Answer, sources []domain.Source) wireBlocks {
	blocks := wireBlocks{
		Work:    "empty",
		Rating:  "empty",
		Body:    "empty",
		Facts:   "empty",
		Answers: "empty",
	}
	switch {
	case work.ID != "" && work.Verified:
		blocks.Work = "verified"
	case work.ID != "" && hasAccepted(sources) && !hasUnverified(sources):
		blocks.Work = "accepted"
	case work.ID != "":
		blocks.Work = "unverified"
	}
	if entry.Rating > 0 {
		blocks.Rating = "set"
	}
	if entry.Body != "" {
		blocks.Body = string(entry.BodyOrigin)
	}
	if entry.Format != "" || entry.FinishedOn != "" {
		blocks.Facts = "parsed"
	}
	answered, skipped := false, false
	for _, answer := range answers {
		if answer.Skipped {
			skipped = true
			continue
		}
		if len(answer.Values) > 0 || answer.Type == domain.AnswerText {
			answered = true
		}
	}
	switch {
	case answered:
		blocks.Answers = "answered"
	case skipped:
		blocks.Answers = "skipped"
	}
	return blocks
}

func hasAccepted(sources []domain.Source) bool {
	for _, source := range sources {
		if source.Trust == domain.TrustAccepted {
			return true
		}
	}
	return false
}

func hasUnverified(sources []domain.Source) bool {
	for _, source := range sources {
		if source.Trust == domain.TrustUnverified {
			return true
		}
	}
	return false
}

// unverifiedOf lists the fields that still need her tap. The engine accepts a whole source, so a card shows one
// marker per source with the id the accept message names (docs/ENTRY_CARD.md 5).
func unverifiedOf(sources []domain.Source) []wireUnverified {
	var out []wireUnverified
	for _, source := range sources {
		if source.Trust != domain.TrustUnverified {
			continue
		}
		value := source.Title
		if source.Year > 0 {
			value = fmt.Sprintf("%s (%d)", source.Title, source.Year)
		}
		field := wireUnverified{
			ID: source.Provider + "|" + source.ProviderID, Field: "work", Value: clipRunes(value, 2000),
		}
		if len(source.Citations) > 0 {
			field.SourceURL = clipRunes(source.Citations[0].URL, 2000)
			field.Snippet = clipRunes(source.Citations[0].Snippet, 2000)
		}
		out = append(out, field)
	}
	return out
}

// whyOf shapes the "why" sheet. A card with nothing to explain has no sheet at all, rather than an empty one.
func whyOf(card session.Card) *wireWhy {
	if len(card.Checks) == 0 && len(card.Tools) == 0 && card.PrereadCost.Total == 0 && card.TurnCost.Total == 0 {
		return nil
	}
	why := &wireWhy{PrereadCost: wireCostOf(card.PrereadCost), TurnCost: wireCostOf(card.TurnCost)}
	for _, check := range card.Checks {
		why.Checks = append(why.Checks, wireWhyCheck{
			Check: check.Name, Option: check.Option, Probability: check.Probability, Fired: check.Fired,
		})
	}
	for _, tool := range card.Tools {
		entry := wireWhyTool{Tool: tool.Tool, OK: tool.OK, Summary: clipRunes(tool.Summary, 500)}
		for _, source := range tool.Sources {
			entry.Sources = append(entry.Sources, wireToolSource{
				URL: clipRunes(source.URL, 2000), Title: clipRunes(source.Title, 300),
			})
		}
		why.Tools = append(why.Tools, entry)
	}
	return why
}

// fidelityOfCard only shows the status; a warning is the notice's job, so the sheet never invents one.
func fidelityOfCard(status string) *wireFidelity {
	if status == "" {
		return nil
	}
	return &wireFidelity{Status: status}
}

// segmentID names a left-out passage by where it sits, so the id survives a reconnect and the include message
// carries enough to find the span again.
func segmentID(segment domain.Segment) string {
	return fmt.Sprintf("seg_%d_%d", segment.Start, segment.End)
}

// parseSegmentID reads the span back. It is deliberately strict: a client can only send an id the card gave it.
func parseSegmentID(id string) (domain.Span, bool) {
	var start, end int
	if _, err := fmt.Sscanf(id, "seg_%d_%d", &start, &end); err != nil {
		return domain.Span{}, false
	}
	if start < 0 || end <= start {
		return domain.Span{}, false
	}
	return domain.Span{Start: start, End: end}, true
}

// clipRunes shortens display text without splitting a character.
func clipRunes(text string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max])
}

// --- the events ------------------------------------------------------------------------------------------

// wireEvent translates one stored event. A draft_snapshot carries the entry as it was; the card around it is read
// now, because the work, the sources and her answers are not versioned with the draft.
func (s *Server) wireEvent(ctx context.Context, ev domain.SessionEvent) ([]byte, error) {
	seq := ev.Seq
	at := ev.CreatedAt
	if ev.Kind == "draft_snapshot" {
		var payload struct {
			Version int          `json:"version"`
			Draft   domain.Entry `json:"draft"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			return nil, fmt.Errorf("server: decode draft_snapshot: %w", err)
		}
		card, err := s.engine.CardFor(ctx, ev.SessionID, payload.Draft, payload.Version)
		if err != nil {
			return nil, err
		}
		return encodeEnvelope(&seq, ev.Kind, ev.SessionID, at, map[string]any{
			"version": payload.Version, "draft": s.wireCardOf(card),
		})
	}
	// Every other persisted payload is already the contract's shape. A field a future writer adds would be caught
	// by the contract test rather than reaching a client.
	var data any
	if len(ev.Payload) == 0 {
		data = struct{}{}
	} else if err := json.Unmarshal(ev.Payload, &data); err != nil {
		return nil, fmt.Errorf("server: decode %s: %w", ev.Kind, err)
	}
	return encodeEnvelope(&seq, ev.Kind, ev.SessionID, at, data)
}

// wireLive turns one live-only agent event into an envelope. It is never stored and never replayed.
func (s *Server) wireLive(ev agent.Event) ([]byte, error) {
	return encodeEnvelope(nil, ev.Kind, ev.SessionID, s.now(), ev.Data)
}

// helloData is what a connecting client gets first (docs/PROTOCOL.md 3).
type helloData struct {
	Session          wireHelloSession      `json:"session"`
	State            string                `json:"state"`
	Draft            *wireCard             `json:"draft"`
	OpenInteractions []wireOpenInteraction `json:"open_interactions"`
	HeadSeq          int                   `json:"head_seq"`
	FullState        bool                  `json:"full_state"`
	ServerVersion    string                `json:"server_version"`
}

type wireHelloSession struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
}

type wireOpenInteraction struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// hello builds the first event. The engine has already decided whether the replay is a full state; this only
// shapes it as the contract says.
func (s *Server) hello(ctx context.Context, hello session.Hello) (wireEnvelope, error) {
	data := helloData{
		Session: wireHelloSession{
			ID: hello.Session.ID, Kind: string(hello.Session.Kind), State: string(hello.Session.State),
		},
		State:            string(hello.Session.State),
		HeadSeq:          hello.HeadSeq,
		FullState:        hello.FullState,
		ServerVersion:    hello.ServerVersion,
		OpenInteractions: []wireOpenInteraction{},
	}
	if hello.Draft != nil {
		card, err := s.engine.Card(ctx, hello.Session.ID)
		if err != nil {
			return wireEnvelope{}, err
		}
		wire := s.wireCardOf(card)
		data.Draft = &wire
	}
	for _, question := range hello.Open {
		payload := question.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}
		data.OpenInteractions = append(data.OpenInteractions, wireOpenInteraction{Kind: question.Kind, Payload: payload})
		if len(data.OpenInteractions) == 8 {
			break // the contract's cap; the card is complete, the rest stay answerable by id
		}
	}
	return wireEnvelope{Seq: nil, Type: "hello", SessionID: hello.Session.ID, TS: instant(s.now()), Data: data}, nil
}

// --- REST shapes -----------------------------------------------------------------------------------------

type wireSession struct {
	ID            string  `json:"id"`
	Kind          string  `json:"kind"`
	State         string  `json:"state"`
	Title         string  `json:"title,omitempty"`
	EntryID       *string `json:"entry_id"`
	DraftVersion  *int    `json:"draft_version"`
	WaitingReason *string `json:"waiting_reason"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

func wireSessionOf(sess domain.Session, waiting string) wireSession {
	out := wireSession{
		ID: sess.ID, Kind: string(sess.Kind), State: string(sess.State), Title: sess.Title,
		CreatedAt: instant(sess.CreatedAt), UpdatedAt: instant(sess.UpdatedAt),
	}
	if sess.EntryID != "" {
		id := sess.EntryID
		out.EntryID = &id
	}
	if sess.EntryID != "" {
		version := sess.DraftVersion
		out.DraftVersion = &version
	}
	if waiting != "" {
		out.WaitingReason = &waiting
	}
	return out
}

type wireEntry struct {
	ID          string       `json:"id"`
	Work        wireWork     `json:"work"`
	RatingQ     int          `json:"rating_q"`
	RatingScale string       `json:"rating_scale"`
	Favourite   bool         `json:"favourite"`
	Verdict     string       `json:"verdict,omitempty"`
	Body        string       `json:"body"`
	BodyOrigin  string       `json:"body_origin"`
	Status      string       `json:"status"`
	Format      string       `json:"format,omitempty"`
	FinishedOn  string       `json:"finished_on,omitempty"`
	Season      *int         `json:"season,omitempty"`
	Answers     []wireAnswer `json:"answers,omitempty"`
	Tags        []wireTag    `json:"tags,omitempty"`
	DeletedAt   string       `json:"deleted_at,omitempty"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
}

func wireEntryOf(view session.EntryView) wireEntry {
	entry := view.Entry
	out := wireEntry{
		ID: entry.ID, Work: wireWorkOf(view.Work), RatingQ: entry.Rating, RatingScale: domain.RatingScale,
		Favourite: entry.Favourite, Verdict: entry.Verdict, Body: entry.Body,
		BodyOrigin: string(entry.BodyOrigin), Status: string(entry.Status), Format: entry.Format,
		FinishedOn: entry.FinishedOn,
		Season:     entry.Season, CreatedAt: instant(entry.CreatedAt), UpdatedAt: instant(entry.UpdatedAt),
	}
	for _, answer := range view.Answers {
		out.Answers = append(out.Answers, wireAnswer{
			QuestionID: answer.QuestionID, Category: answer.Category, Prompt: answer.Prompt,
			AnswerType: string(answer.Type), Values: answer.Values, Skipped: answer.Skipped,
		})
	}
	for _, tag := range view.Tags {
		out.Tags = append(out.Tags, wireTag{Name: clipRunes(tag.Name, 100), Source: tag.Source})
	}
	return out
}

type wireCitation struct {
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

type wireSource struct {
	Provider   string         `json:"provider"`
	ProviderID string         `json:"provider_id,omitempty"`
	Trust      string         `json:"trust"`
	Citations  []wireCitation `json:"citations,omitempty"`
}

type wireWorkDetail struct {
	wireWork
	Sources []wireSource `json:"sources"`
}

func wireWorkDetailOf(view session.WorkView) wireWorkDetail {
	out := wireWorkDetail{wireWork: wireWorkOf(view.Work), Sources: []wireSource{}}
	for _, source := range view.Sources {
		item := wireSource{Provider: source.Provider, ProviderID: source.ProviderID, Trust: string(source.Trust)}
		for _, citation := range source.Citations {
			item.Citations = append(item.Citations, wireCitation{
				URL: clipRunes(citation.URL, 2000), Snippet: clipRunes(citation.Snippet, 2000),
			})
		}
		out.Sources = append(out.Sources, item)
	}
	return out
}

type wireRevision struct {
	ID        string    `json:"id"`
	EntryID   string    `json:"entry_id"`
	Revision  int       `json:"revision"`
	Snapshot  wireEntry `json:"snapshot"`
	CreatedAt string    `json:"created_at"`
}

// wireRevisionOf shapes a revision. The snapshot is rendered with the entry's own work, so a revision screen can
// show what the review was about without a second request.
func wireRevisionOf(revision domain.Revision, work domain.Work, answers []domain.Answer) wireRevision {
	return wireRevision{
		ID: revision.ID, EntryID: revision.EntryID, Revision: revision.Number,
		Snapshot:  wireEntryOf(session.EntryView{Entry: revision.Snapshot, Work: work, Answers: answers}),
		CreatedAt: instant(revision.CreatedAt),
	}
}
