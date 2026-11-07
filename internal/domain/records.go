package domain

import (
	"encoding/json"
	"time"
)

// The remaining shapes the data layer stores. They live here because they are plain data about a review, and the
// store, the session engine and the exporter all have to agree on them.

// AnswerType is the shape of an answer.
type AnswerType string

const (
	// AnswerTags is a set of descriptor chips, stored by stable value.
	AnswerTags AnswerType = "tags"
	// AnswerChoice is one of a question's options.
	AnswerChoice AnswerType = "choice"
	// AnswerText is a short free-text answer.
	AnswerText AnswerType = "short_text"
)

// Answer is one blank the agent asked about. The prompt is stored with the answer on purpose: the question set will
// change, and an old review must still make sense (PLAN.md 6).
type Answer struct {
	ID         string     `json:"id"`
	EntryID    string     `json:"entry_id"`
	QuestionID string     `json:"question_id"`
	Category   string     `json:"category,omitempty"`
	Prompt     string     `json:"prompt"`
	Type       AnswerType `json:"answer_type"`
	Values     []string   `json:"values"`
	Skipped    bool       `json:"skipped"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Tag is a genre from a provider or a tag she added herself. The descriptor chips she picked are answers, not tags
// (docs/ENTRY_CARD.md 1).
type Tag struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// Revision is one previous version of an entry, kept forever and never changed.
type Revision struct {
	ID        string    `json:"id"`
	EntryID   string    `json:"entry_id"`
	Number    int       `json:"revision"`
	Snapshot  Entry     `json:"snapshot"`
	CreatedAt time.Time `json:"created_at"`
}

// SessionKind is what a session is for.
type SessionKind string

const (
	// SessionEntry is one short session per entry.
	SessionEntry SessionKind = "entry"
	// SessionGeneral is the library chat.
	SessionGeneral SessionKind = "general"
)

// SessionState is where a session is in its life.
type SessionState string

const (
	// SessionActive is a session that may still be resumed.
	SessionActive SessionState = "active"
	// SessionSaved is a session whose entry was confirmed.
	SessionSaved SessionState = "saved"
	// SessionDiscarded is a session whose draft was thrown away.
	SessionDiscarded SessionState = "discarded"
)

// Session is the app's own conversation. Nova's sessions are not importable, so this is ours to store and resume
// (PLAN.md 2).
type Session struct {
	ID      string       `json:"id"`
	Kind    SessionKind  `json:"kind"`
	Title   string       `json:"title,omitempty"`
	EntryID string       `json:"entry_id,omitempty"`
	State   SessionState `json:"state"`
	// DraftVersion increments on every change to the card, in the transaction that makes the change. A client
	// edit that names an older version is rejected without changing anything (docs/PROTOCOL.md 6).
	DraftVersion int             `json:"draft_version"`
	Messages     json.RawMessage `json:"messages,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// ResolvedBy says who answered an interaction.
type ResolvedBy string

const (
	// ResolvedByUser is her tap.
	ResolvedByUser ResolvedBy = "user"
	// ResolvedByServer is the service withdrawing a question that no longer applies.
	ResolvedByServer ResolvedBy = "server"
)

// Interaction is one question the service asked. It is written when opened, resolved once, and never deleted: the
// record of what was asked is part of the review's history.
type Interaction struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"session_id"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload"`
	OpenedSeq   int             `json:"opened_seq"`
	OpenedAt    time.Time       `json:"opened_at"`
	ResolvedSeq *int            `json:"resolved_seq,omitempty"`
	ResolvedBy  ResolvedBy      `json:"resolved_by,omitempty"`
	Resolution  json.RawMessage `json:"resolution,omitempty"`
}

// Open reports whether the interaction is still waiting.
func (i Interaction) Open() bool { return i.ResolvedSeq == nil }

// Cost sources are nova's vocabulary for where a dollar figure came from, not Sepia's. A call's CostUSD is what it
// consumed either way; the source says whose dollars they were, so a number the provider reported is never shown as
// a spend that only a price list estimated, and an allowance is not shown as money spent.
const (
	// CostSourceProvider is a figure the provider itself reported. It is the strongest word, and real spend.
	CostSourceProvider = "provider"
	// CostSourceAllowance is a figure the provider reported as consumed from a plan's allowance.
	CostSourceAllowance = "allowance"
	// CostSourceCatalog is Sepia's own price-list estimate, because the provider reported no figure at all.
	CostSourceCatalog = "catalog"
)

// CostSummary is money a session spent, with the part a plan's allowance covered split out. Covered is part of Total,
// never in addition to it, so Spent plus Covered is the whole. Estimated is true when any of that money came from a
// price-list estimate rather than a provider's own report.
type CostSummary struct {
	Total     float64 `json:"total_usd"`
	Covered   float64 `json:"covered_usd"`
	Estimated bool    `json:"estimated,omitempty"`
}

// Spent is the money that left an account: the total minus what an allowance covered.
func (c CostSummary) Spent() float64 { return c.Total - c.Covered }

// Add sums two summaries, which is how a session's total is built from per-call rows.
func (c CostSummary) Add(other CostSummary) CostSummary {
	c.Total += other.Total
	c.Covered += other.Covered
	c.Estimated = c.Estimated || other.Estimated
	return c
}

// CostOf turns one ledger row's money into a summary, so the caller does not repeat the same three lines.
func CostOf(total, covered float64, source string) CostSummary {
	return CostSummary{Total: total, Covered: covered, Estimated: source == CostSourceCatalog}
}

// DecisionCall is one recorded decisions call: the odds, what fired, what it cost. It holds no text, so the
// ledger can never become a copy of her message (docs/BUILD.md invariant 5).
type DecisionCall struct {
	ID           string          `json:"id"`
	SessionID    string          `json:"session_id,omitempty"`
	MessageIndex int             `json:"message_index"`
	Backend      string          `json:"backend"`
	Model        string          `json:"model"`
	Odds         json.RawMessage `json:"odds"`
	Fired        []string        `json:"fired"`
	CostUSD      float64         `json:"cost_usd"`
	// AllowanceUSD is the part of CostUSD a plan's allowance paid for, and CostSource is where the figure came from.
	AllowanceUSD float64   `json:"allowance_usd,omitempty"`
	CostSource   string    `json:"cost_source,omitempty"`
	LatencyMS    int       `json:"latency_ms"`
	CreatedAt    time.Time `json:"created_at"`
}

// Cost is the row's money as a summary, which is what the "why" sheet and the running total want.
func (d DecisionCall) Cost() CostSummary { return CostOf(d.CostUSD, d.AllowanceUSD, d.CostSource) }

// TurnCall is one recorded model turn: the tokens and money it cost and how it ended. It holds no text either.
type TurnCall struct {
	ID               string  `json:"id"`
	SessionID        string  `json:"session_id,omitempty"`
	TurnIndex        int     `json:"turn_index"`
	Model            string  `json:"model"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	// AllowanceUSD is the part of CostUSD a plan's allowance paid for, and CostSource is where the figure came from.
	AllowanceUSD float64   `json:"allowance_usd,omitempty"`
	CostSource   string    `json:"cost_source,omitempty"`
	LatencyMS    int       `json:"latency_ms"`
	Reason       string    `json:"reason,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Cost is the row's money as a summary.
func (t TurnCall) Cost() CostSummary { return CostOf(t.CostUSD, t.AllowanceUSD, t.CostSource) }

// ClientMessage is one row of the idempotency ledger: the first outcome of one client_msg_id.
type ClientMessage struct {
	SessionID   string    `json:"session_id"`
	ClientMsgID string    `json:"client_msg_id"`
	Seq         int       `json:"seq"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// SessionEvent is one entry in the per-session log a reconnecting client resumes from.
type SessionEvent struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Seq       int             `json:"seq"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// Cursor is an opaque position in a list. It is never an offset: a list that shifts under a reader must not repeat or
// skip rows (docs/STANDARDS.md 4).
type Cursor struct {
	// FinishedOn is the local date of the last row on the previous page, for the library.
	FinishedOn string
	// ID is the last row's id, which breaks ties on the date.
	ID string
}

// Empty reports whether this is the first page.
func (c Cursor) Empty() bool { return c.ID == "" && c.FinishedOn == "" }
