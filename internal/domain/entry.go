package domain

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// An entry is the operator's review. Two things are required to save it, and both are enforced here as well as in
// the confirm button: a rating, and a non-empty body made of her own words (docs/ENTRY_CARD.md 2). Everything
// else is optional, so skipping every question still saves.

// BodyOrigin says where the body came from, which decides what the save guard has to prove.
type BodyOrigin string

const (
	// BodyExtracted means the body is spans located in the operator's messages.
	BodyExtracted BodyOrigin = "extracted"
	// BodyTyped means she typed it on the card, which is her own input.
	BodyTyped BodyOrigin = "typed"
	// BodyMixed means an extracted body she then edited.
	BodyMixed BodyOrigin = "mixed"
	// BodyImported means it came from a transcription of her own screenshots.
	BodyImported BodyOrigin = "imported"
)

// BlockState is how much a field on the card can be trusted.
type BlockState string

const (
	// StateVerified came from an API that is canonical for the medium.
	StateVerified BlockState = "verified"
	// StateUnverified came from the web or a manual entry, and needs a tap.
	StateUnverified BlockState = "unverified"
	// StateAccepted is a web or image field the operator tapped to accept.
	StateAccepted BlockState = "accepted"
	// StateImageRead was read from a screenshot and needs a tap.
	StateImageRead BlockState = "image_read"
)

// Status is an entry's lifecycle. A draft never appears in the library or an export.
type Status string

const (
	// StatusDraft is an unsaved draft.
	StatusDraft Status = "draft"
	// StatusSaved is a confirmed entry.
	StatusSaved Status = "saved"
)

const (
	// MaxBodyLength is the body limit in docs/STANDARDS.md 4a.
	MaxBodyLength = 8000
	// dateLayout is the stored local date, with no time and no zone.
	dateLayout = "2006-01-02"
)

// Entry is one review of one work. It is the shape both the draft card and the saved entry use.
type Entry struct {
	ID           string     `json:"id"`
	WorkID       string     `json:"work_id"`
	Rating       int        `json:"rating_q"`
	RatingScale  string     `json:"rating_scale"`
	Favourite    bool       `json:"favourite"`
	Season       *int       `json:"season,omitempty"`
	Verdict      string     `json:"verdict,omitempty"`
	Body         string     `json:"body"`
	BodyOrigin   BodyOrigin `json:"body_origin"`
	BodySpans    []Span     `json:"body_spans,omitempty"`
	ImportedFrom string     `json:"imported_from,omitempty"`
	Format       string     `json:"format,omitempty"`
	FinishedOn   string     `json:"finished_on,omitempty"`
	Status       Status     `json:"status"`
	Deleted      bool       `json:"deleted,omitempty"`
}

// GuardError is a refusal with a stable code the UI maps to its own copy.
type GuardError struct {
	Code    string
	Message string
}

func (e *GuardError) Error() string { return e.Code + ": " + e.Message }

// The stable codes a save can be refused with.
const (
	CodeRatingRequired     = "rating_required"
	CodeBodyRequired       = "body_required"
	CodeBodyTooLong        = "body_too_long"
	CodeBodyNotYourWords   = "body_not_your_words"
	CodeBodyOriginInvalid  = "body_origin_invalid"
	CodeVerdictTooLong     = "verdict_too_long"
	CodeVerdictNotYourWord = "verdict_not_your_words"
	CodeDateInvalid        = "date_invalid"
)

// ValidateForSave is the rule the whole project exists for: nothing saves without a rating and a body made of the
// operator's own words. sourceText is the concatenation of her messages in the session; it is empty for a typed
// or imported body, which needs no proof because she supplied it.
func ValidateForSave(e Entry, sourceText string) error {
	if !ValidRating(e.Rating) {
		return &GuardError{Code: CodeRatingRequired, Message: "Add a rating before saving."}
	}
	if strings.TrimSpace(e.Body) == "" {
		return &GuardError{Code: CodeBodyRequired, Message: "Add a few words of your own before saving."}
	}
	if len(e.Body) > MaxBodyLength {
		return &GuardError{Code: CodeBodyTooLong, Message: "That is longer than a review can be."}
	}
	if err := validOrigin(e.BodyOrigin); err != nil {
		return err
	}

	switch e.BodyOrigin {
	case BodyExtracted:
		if len(e.BodySpans) == 0 {
			return &GuardError{Code: CodeBodyNotYourWords, Message: "The body has to come from what you wrote."}
		}
		if !SpansContained(sourceText, e.BodySpans) {
			return &GuardError{Code: CodeBodyNotYourWords, Message: "The body has to come from what you wrote."}
		}
		if Compose(sourceText, e.BodySpans) != e.Body {
			return &GuardError{Code: CodeBodyNotYourWords, Message: "The body has to be exactly the words you wrote."}
		}
	case BodyMixed:
		if len(e.BodySpans) == 0 || !SpansContained(sourceText, e.BodySpans) {
			return &GuardError{Code: CodeBodyNotYourWords, Message: "The extracted part of the body has to come from what you wrote."}
		}
	case BodyTyped, BodyImported:
		// Her own typing, or a transcription of her own screenshot that she accepted. Nothing to prove here.
	}

	if e.Verdict != "" {
		if len(e.Verdict) > MaxVerdictLength {
			return &GuardError{Code: CodeVerdictTooLong, Message: "A verdict is one short line."}
		}
		if e.BodyOrigin == BodyExtracted && !ContainedIn(sourceText, e.Verdict) {
			return &GuardError{Code: CodeVerdictNotYourWord, Message: "The verdict has to come from what you wrote."}
		}
	}

	if e.FinishedOn != "" {
		if _, err := time.Parse(dateLayout, e.FinishedOn); err != nil {
			return &GuardError{Code: CodeDateInvalid, Message: "That is not a date."}
		}
	}
	return nil
}

// validOrigin rejects an unknown or empty origin: an entry always knows where its body came from.
func validOrigin(origin BodyOrigin) error {
	switch origin {
	case BodyExtracted, BodyTyped, BodyMixed, BodyImported:
		return nil
	default:
		return &GuardError{Code: CodeBodyOriginInvalid, Message: "The body does not say where it came from."}
	}
}

// CanConfirm reports whether the confirm button may be enabled, and which requirements are still missing. The
// button and the save guard ask the same question, so they cannot disagree.
func CanConfirm(e Entry, sourceText string) (bool, []string) {
	var guard *GuardError
	if errors.As(ValidateForSave(e, sourceText), &guard) {
		return false, []string{guard.Code}
	}
	return true, nil
}

// Change is one difference between two versions of an entry, for the edit screen.
type Change struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Diff lists what an edit changed, in a stable order. It is how the revision screen explains itself.
func Diff(before, after Entry) []Change {
	var out []Change
	add := func(field, b, a string) {
		if b != a {
			out = append(out, Change{Field: field, Before: b, After: a})
		}
	}

	add("rating", FormatRating(before.Rating), FormatRating(after.Rating))
	add("body", before.Body, after.Body)
	add("verdict", before.Verdict, after.Verdict)
	add("format", before.Format, after.Format)
	add("finished_on", before.FinishedOn, after.FinishedOn)
	add("favourite", boolString(before.Favourite), boolString(after.Favourite))
	add("season", seasonString(before.Season), seasonString(after.Season))
	return out
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func seasonString(s *int) string {
	if s == nil {
		return ""
	}
	return strconv.Itoa(*s)
}
