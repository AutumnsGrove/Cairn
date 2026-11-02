package agent

import (
	"time"

	"github.com/AutumnsGrove/cairn/internal/domain"
)

// Parsed is the deterministic read of one message: the rating, the date and the format cues. It is plain rules,
// never a model, so the same words always produce the same fields (PLAN.md 5, step 3).
type Parsed struct {
	Rating  domain.Rating
	Date    domain.DateResult
	Formats []string
}

// parseMessage reads the fields code can prove. A vague rating stays vague, so the card asks instead of guessing.
func parseMessage(text string, now time.Time, clientDate, timezone string) Parsed {
	loc := time.UTC
	if timezone != "" {
		if loaded, err := time.LoadLocation(timezone); err == nil {
			loc = loaded
		}
	}
	base := now
	if clientDate != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", clientDate, loc); err == nil {
			base = parsed
		}
	}
	return Parsed{
		Rating:  domain.ParseRating(text),
		Date:    domain.ResolveDate(text, base, loc),
		Formats: domain.FormatCues(text),
	}
}

// RatingQuarters is the value to put on the card: 0 for none, a vague phrase, or an out-of-range value. The card
// shows what was stored, so a snapped value is visible rather than hidden.
func (p Parsed) RatingQuarters() int {
	switch p.Rating.Status {
	case domain.RatingStated, domain.RatingSnapped:
		if domain.ValidRating(p.Rating.Quarters) {
			return p.Rating.Quarters
		}
		return 0
	default:
		return 0
	}
}

// DateString is the local day to store. An ambiguous date is left empty, because guessing between two days is
// worse than asking.
func (p Parsed) DateString() string {
	if p.Date.Confidence != domain.DateClear || p.Date.Date.IsZero() {
		return ""
	}
	return p.Date.Date.Format("2006-01-02")
}

// FormatString is the first cue, which is the most specific in the cue order. An empty result is normal.
func (p Parsed) FormatString() string {
	if len(p.Formats) == 0 {
		return ""
	}
	return p.Formats[0]
}
