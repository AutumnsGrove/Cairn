package domain

import (
	"errors"
	"strings"
	"testing"
)

// The save guard is the last line of invariant 1 and invariant 2: a body of her own words, and a rating, or
// nothing saves. These tests are the ones a reviewer should read first.

const session = "hey so i finished watching dune last night, loved it honestly, easy 4.5, " +
	"the world felt so lived in but the middle dragged a bit. would watch again."

func extractedEntry(t *testing.T) Entry {
	t.Helper()
	got := Locate(session, []string{"loved it honestly, easy 4.5", "the world felt so lived in"})
	if len(got.Spans) != 2 {
		t.Fatalf("fixture spans = %+v", got.Spans)
	}
	return Entry{
		Rating:      18,
		RatingScale: RatingScale,
		Body:        Compose(session, got.Spans),
		BodyOrigin:  BodyExtracted,
		BodySpans:   got.Spans,
		Verdict:     "would watch again",
		Format:      FormatStreaming,
		FinishedOn:  "2026-10-29",
		Status:      StatusDraft,
	}
}

func TestValidateForSaveHappy(t *testing.T) {
	e := extractedEntry(t)
	if err := ValidateForSave(e, session); err != nil {
		t.Fatalf("a valid entry was refused: %v", err)
	}
	ok, missing := CanConfirm(e, session)
	if !ok || len(missing) != 0 {
		t.Fatalf("CanConfirm = %v %v, want true", ok, missing)
	}
}

func TestValidateForSaveRequiresARating(t *testing.T) {
	e := extractedEntry(t)
	e.Rating = 0
	err := ValidateForSave(e, session)
	var guard *GuardError
	if !errors.As(err, &guard) || guard.Code != CodeRatingRequired {
		t.Fatalf("err = %v, want %s", err, CodeRatingRequired)
	}
	if ok, missing := CanConfirm(e, session); ok || missing[0] != CodeRatingRequired {
		t.Errorf("CanConfirm = %v %v", ok, missing)
	}
}

func TestValidateForSaveRequiresABody(t *testing.T) {
	e := extractedEntry(t)
	e.Body = "   \n "
	e.BodySpans = nil
	err := ValidateForSave(e, session)
	var guard *GuardError
	if !errors.As(err, &guard) || guard.Code != CodeBodyRequired {
		t.Fatalf("err = %v, want %s", err, CodeBodyRequired)
	}
}

// The heart of it: an extracted body that is not in her messages is refused, and so is a body that does not match
// the spans it claims to be made of.
func TestValidateForSaveRefusesAnInventedBody(t *testing.T) {
	e := extractedEntry(t)
	e.Body = "a masterclass in restrained world-building"
	if err := ValidateForSave(e, session); err == nil {
		t.Fatal("an invented body was accepted")
	}

	e = extractedEntry(t)
	e.Body = "loved it honestly" // a real fragment, but not what the spans say
	if err := ValidateForSave(e, session); err == nil {
		t.Fatal("a body that does not match its spans was accepted")
	}
}

func TestValidateForSaveRefusesSpansFromAnotherSession(t *testing.T) {
	e := extractedEntry(t)
	if err := ValidateForSave(e, "a completely different conversation"); err == nil {
		t.Fatal("spans from another session were accepted")
	}
}

// Typing the body is always allowed: it is her own input, so there is nothing to prove.
func TestValidateForSaveAcceptsATypedBody(t *testing.T) {
	e := Entry{
		Rating:      18,
		RatingScale: RatingScale,
		Body:        "I loved the world and the pacing lost me in the middle.",
		BodyOrigin:  BodyTyped,
	}
	if err := ValidateForSave(e, ""); err != nil {
		t.Fatalf("a typed body was refused: %v", err)
	}
}

// A mixed body keeps the extracted spans provable while allowing her edits.
func TestValidateForSaveAcceptsAMixedBody(t *testing.T) {
	e := extractedEntry(t)
	e.BodyOrigin = BodyMixed
	e.Body = e.Body + " And I would watch it again."
	if err := ValidateForSave(e, session); err != nil {
		t.Fatalf("a mixed body was refused: %v", err)
	}

	e.BodySpans = []Span{{Start: 0, End: 5, Text: "hello"}}
	if err := ValidateForSave(e, session); err == nil {
		t.Fatal("a mixed body with false spans was accepted")
	}
}

// An imported body comes from a transcription of her own screenshot, so the origin must say so.
func TestValidateForSaveAcceptsAnImportedBody(t *testing.T) {
	e := Entry{
		Rating:       16,
		RatingScale:  RatingScale,
		Body:         "A quiet book about grief.",
		BodyOrigin:   BodyImported,
		ImportedFrom: "fable",
	}
	if err := ValidateForSave(e, ""); err != nil {
		t.Fatalf("an imported body was refused: %v", err)
	}
}

func TestValidateForSaveRefusesAnUnknownOrigin(t *testing.T) {
	e := extractedEntry(t)
	e.BodyOrigin = "guessed"
	var guard *GuardError
	if err := ValidateForSave(e, session); !errors.As(err, &guard) || guard.Code != CodeBodyOriginInvalid {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateForSaveRefusesAnInventedVerdict(t *testing.T) {
	e := extractedEntry(t)
	e.Verdict = "the best film of the decade"
	if err := ValidateForSave(e, session); err == nil {
		t.Fatal("an invented verdict was accepted")
	}

	e = extractedEntry(t)
	e.Verdict = strings.Repeat("very ", 100)
	if err := ValidateForSave(e, session); err == nil {
		t.Fatal("an over-long verdict was accepted")
	}
}

func TestValidateForSaveRefusesAnOverLongBody(t *testing.T) {
	e := Entry{
		Rating:      18,
		RatingScale: RatingScale,
		Body:        strings.Repeat("a", MaxBodyLength+1),
		BodyOrigin:  BodyTyped,
	}
	var guard *GuardError
	if err := ValidateForSave(e, ""); !errors.As(err, &guard) || guard.Code != CodeBodyTooLong {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateForSaveRefusesABadDate(t *testing.T) {
	e := extractedEntry(t)
	e.FinishedOn = "29 October 2026"
	var guard *GuardError
	if err := ValidateForSave(e, session); !errors.As(err, &guard) || guard.Code != CodeDateInvalid {
		t.Fatalf("err = %v", err)
	}
}

func TestDiff(t *testing.T) {
	before := extractedEntry(t)
	after := before
	after.Rating = 20
	after.Body = "I loved the world and the pacing lost me in the middle."
	after.BodyOrigin = BodyTyped
	after.BodySpans = nil

	changes := Diff(before, after)
	if len(changes) != 2 {
		t.Fatalf("changes = %+v, want rating and body", changes)
	}
	if changes[0].Field != "rating" || changes[0].Before != "4.5" || changes[0].After != "5" {
		t.Errorf("first change = %+v", changes[0])
	}
	if changes[1].Field != "body" {
		t.Errorf("second change = %+v", changes[1])
	}
}

func TestDiffOfAnUnchangedEntry(t *testing.T) {
	e := extractedEntry(t)
	if changes := Diff(e, e); len(changes) != 0 {
		t.Fatalf("changes = %+v, want none", changes)
	}
}

func TestDiffSeason(t *testing.T) {
	one := 1
	before := extractedEntry(t)
	after := before
	after.Season = &one
	changes := Diff(before, after)
	if len(changes) != 1 || changes[0].Field != "season" || changes[0].After != "1" {
		t.Fatalf("changes = %+v", changes)
	}
}
