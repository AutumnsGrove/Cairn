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
	ID        string          `json:"id"`
	Kind      SessionKind     `json:"kind"`
	Title     string          `json:"title,omitempty"`
	EntryID   string          `json:"entry_id,omitempty"`
	State     SessionState    `json:"state"`
	Messages  json.RawMessage `json:"messages,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
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
