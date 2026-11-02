package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/cairn/internal/domain"
)

// The repository tests drive the real store against the real schema. They cover the happy route (a review saved and
// read back), the bad route (a confirm that should be refused) and the sad route (a missing row), plus the two
// properties the layer promises: a revision appears exactly when an entry is confirmed over an existing one, and a
// page cursor neither repeats nor skips a row.

func sampleEntry(id, workID string, rating int) domain.Entry {
	return domain.Entry{
		ID: id, WorkID: workID, Rating: rating, RatingScale: domain.RatingScale,
		Body: "the world felt so lived in", BodyOrigin: domain.BodyTyped,
		Format: domain.FormatPaperback, FinishedOn: "2026-10-29", Status: domain.StatusDraft,
	}
}

func sampleWork(id string) domain.Work {
	return domain.Work{
		ID: id, Medium: "book", Title: "Dune", Year: 1965,
		Creators: []domain.Creator{{Role: "author", Name: "Frank Herbert"}},
		Details:  map[string]string{"pages": "412"},
		Verified: true, PrimaryProvider: "openlibrary", PrimaryID: "OL893415W",
	}
}

func TestWorkRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	w := sampleWork("w1")
	if err := s.SaveWork(ctx, w); err != nil {
		t.Fatalf("SaveWork: %v", err)
	}
	got, err := s.Work(ctx, "w1")
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if got.Title != "Dune" || got.Year != 1965 || !got.Verified {
		t.Fatalf("work = %+v", got)
	}
	if len(got.Creators) != 1 || got.Creators[0].Name != "Frank Herbert" {
		t.Fatalf("creators = %+v", got.Creators)
	}
	if got.Details["pages"] != "412" {
		t.Fatalf("details = %+v", got.Details)
	}

	// Saving again updates rather than failing on the primary key.
	w.Title = "Dune (1965)"
	if err := s.SaveWork(ctx, w); err != nil {
		t.Fatalf("SaveWork again: %v", err)
	}
	if got, _ := s.Work(ctx, "w1"); got.Title != "Dune (1965)" {
		t.Fatalf("title = %q after update", got.Title)
	}

	if _, err := s.Work(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSourceRoundTripAndAccept(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.SaveWork(ctx, sampleWork("w1")); err != nil {
		t.Fatal(err)
	}

	id, err := s.AddSource(ctx, "w1", domain.Source{
		Provider: "web", Trust: domain.TrustUnverified, Title: "Dune",
		Citations: []domain.Citation{{URL: "https://example.test/dune", Snippet: "a 1965 novel"}},
	})
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	sources, err := s.Sources(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Trust != domain.TrustUnverified {
		t.Fatalf("sources = %+v", sources)
	}
	if len(sources[0].Citations) != 1 || sources[0].Citations[0].URL == "" {
		t.Fatalf("citations were lost: %+v", sources[0].Citations)
	}

	if err := s.AcceptSource(ctx, id); err != nil {
		t.Fatalf("AcceptSource: %v", err)
	}
	// The database allows one accept; the second is a change to an accepted row and must fail.
	if err := s.AcceptSource(ctx, id); err == nil {
		t.Fatal("a source was accepted twice through the repository")
	}
}

func TestCreateDraftAndSaveWithRevision(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.SaveWork(ctx, sampleWork("w1")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDraft(ctx, sampleEntry("e1", "w1", 18)); err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}

	drafts, err := s.Drafts(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].ID != "e1" {
		t.Fatalf("drafts = %+v", drafts)
	}

	// The first save has nothing to revise.
	rev, err := s.SaveEntry(ctx, sampleEntry("e1", "w1", 18))
	if err != nil {
		t.Fatalf("SaveEntry: %v", err)
	}
	if rev != 0 {
		t.Fatalf("revision = %d, want none for a first save", rev)
	}

	saved, err := s.Entry(ctx, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != domain.StatusSaved {
		t.Fatalf("status = %q", saved.Status)
	}
	if drafts, _ := s.Drafts(ctx, 10); len(drafts) != 0 {
		t.Fatalf("the draft is still listed: %+v", drafts)
	}

	// A second save over a saved entry writes a revision of what it replaced.
	edited := sampleEntry("e1", "w1", 20)
	edited.Body = "the world felt so lived in, and the ending landed"
	rev, err = s.SaveEntry(ctx, edited)
	if err != nil {
		t.Fatalf("SaveEntry (edit): %v", err)
	}
	if rev != 1 {
		t.Fatalf("revision = %d, want 1", rev)
	}

	revisions, err := s.Revisions(ctx, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 1 {
		t.Fatalf("revisions = %+v", revisions)
	}
	if revisions[0].Snapshot.Rating != 18 {
		t.Fatalf("the revision kept %d quarters, want the previous 18", revisions[0].Snapshot.Rating)
	}
	if revisions[0].Snapshot.Body != "the world felt so lived in" {
		t.Fatalf("the revision kept %q", revisions[0].Snapshot.Body)
	}
}

// Property: a revision exists exactly when a save replaces an existing entry, never for the first save, and the
// number only goes up.
func TestRevisionNumbersOnlyGoUp(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.SaveWork(ctx, sampleWork("w1")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDraft(ctx, sampleEntry("e1", "w1", 18)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveEntry(ctx, sampleEntry("e1", "w1", 18)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		rev, err := s.SaveEntry(ctx, sampleEntry("e1", "w1", 18+i))
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		if rev != i {
			t.Fatalf("revision = %d, want %d", rev, i)
		}
	}
	revisions, err := s.Revisions(ctx, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 3 {
		t.Fatalf("%d revisions, want 3", len(revisions))
	}
	if revisions[0].Number != 3 || revisions[2].Number != 1 {
		t.Fatalf("revisions are not newest first: %+v", revisions)
	}
}

func TestLibraryPaginationNeitherRepeatsNorSkips(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.SaveWork(ctx, sampleWork("w1")); err != nil {
		t.Fatal(err)
	}

	// Twelve saved entries, three of them on the same day, so the tiebreaker is exercised.
	for i := 0; i < 12; i++ {
		e := sampleEntry("e"+string(rune('a'+i)), "w1", 18)
		e.Status = domain.StatusSaved
		e.FinishedOn = "2026-10-2" + string(rune('0'+i%3))
		if err := s.CreateDraft(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	var cursor domain.Cursor
	pages := 0
	for {
		page, next, err := s.Library(ctx, cursor, 5)
		if err != nil {
			t.Fatalf("Library: %v", err)
		}
		pages++
		for _, e := range page {
			if seen[e.ID] {
				t.Fatalf("%s appeared twice", e.ID)
			}
			seen[e.ID] = true
		}
		if next.Empty() {
			break
		}
		cursor = next
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != 12 {
		t.Fatalf("saw %d entries, want 12", len(seen))
	}
	if pages != 3 {
		t.Fatalf("%d pages, want 3", pages)
	}
}

func TestLibraryHidesDeletedAndDrafts(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.SaveWork(ctx, sampleWork("w1")); err != nil {
		t.Fatal(err)
	}
	saved := sampleEntry("e1", "w1", 18)
	saved.Status = domain.StatusSaved
	draft := sampleEntry("e2", "w1", 18)
	if err := s.CreateDraft(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDraft(ctx, draft); err != nil {
		t.Fatal(err)
	}

	page, _, err := s.Library(ctx, domain.Cursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != "e1" {
		t.Fatalf("library = %+v", page)
	}

	if delErr := s.SoftDeleteEntry(ctx, "e1"); delErr != nil {
		t.Fatalf("SoftDeleteEntry: %v", delErr)
	}
	page, _, err = s.Library(ctx, domain.Cursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 0 {
		t.Fatalf("a deleted entry is still in the library: %+v", page)
	}
	if got, entryErr := s.Entry(ctx, "e1"); entryErr != nil || !got.Deleted {
		t.Fatalf("the deleted entry is gone from storage: %+v %v", got, entryErr)
	}

	if restoreErr := s.RestoreEntry(ctx, "e1"); restoreErr != nil {
		t.Fatalf("RestoreEntry: %v", restoreErr)
	}
	page, _, err = s.Library(ctx, domain.Cursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 {
		t.Fatalf("the restored entry is missing: %+v", page)
	}
}

func TestAnswersRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.SaveWork(ctx, sampleWork("w1")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDraft(ctx, sampleEntry("e1", "w1", 18)); err != nil {
		t.Fatal(err)
	}

	answers := []domain.Answer{
		{
			QuestionID: "setting", Category: "Setting and world", Prompt: "How was the world?",
			Type: domain.AnswerTags, Values: []string{"immersive", "lived-in"},
		},
		{QuestionID: "recommend", Prompt: "Recommend it?", Type: domain.AnswerChoice, Values: []string{"yes"}},
		{QuestionID: "note", Prompt: "Anything else?", Type: domain.AnswerText, Skipped: true},
	}
	if err := s.AddAnswers(ctx, "e1", answers); err != nil {
		t.Fatalf("AddAnswers: %v", err)
	}

	got, err := s.Answers(ctx, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("answers = %+v", got)
	}
	if got[0].Values[1] != "lived-in" || got[0].Category != "Setting and world" {
		t.Fatalf("first answer = %+v", got[0])
	}
	if !got[2].Skipped {
		t.Fatalf("the skipped answer lost its flag: %+v", got[2])
	}
}

func TestSessionAndEventLog(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	if err := s.CreateSession(ctx, domain.Session{ID: "sess", Kind: domain.SessionEntry, State: domain.SessionActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	first, err := s.AppendEvent(ctx, "sess", "user_message", []byte(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if first.Seq != 1 {
		t.Fatalf("first seq = %d", first.Seq)
	}
	second, err := s.AppendEvent(ctx, "sess", "text_delta", []byte(`{"text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if second.Seq != 2 {
		t.Fatalf("second seq = %d", second.Seq)
	}

	head, err := s.HeadSequence(ctx, "sess")
	if err != nil {
		t.Fatal(err)
	}
	if head != 2 {
		t.Fatalf("head = %d", head)
	}

	// A client that saw seq 1 gets exactly the events after it.
	replay, err := s.EventsSince(ctx, "sess", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].Seq != 2 || replay[0].Kind != "text_delta" {
		t.Fatalf("replay = %+v", replay)
	}
	if string(replay[0].Payload) != `{"text":"hello"}` {
		t.Fatalf("payload = %s", replay[0].Payload)
	}

	// Compaction removes the streamed chunks of a finished turn and nothing else.
	if compactErr := s.CompactTextDeltas(ctx, "sess", 3); compactErr != nil {
		t.Fatal(compactErr)
	}
	replay, err = s.EventsSince(ctx, "sess", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].Kind != "user_message" {
		t.Fatalf("after compaction = %+v", replay)
	}

	if stateErr := s.SetSessionState(ctx, "sess", domain.SessionSaved); stateErr != nil {
		t.Fatal(stateErr)
	}
	if active, _ := s.ActiveSessions(ctx, 10); len(active) != 0 {
		t.Fatalf("a saved session is still active: %+v", active)
	}
}

func TestSessionNotFound(t *testing.T) {
	s := openTest(t)
	if _, err := s.Session(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := s.Entry(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Every list query must use an index, and must not fall back to a temporary sort: a library that sorts on every
// page is the difference between fast and unusable at a few thousand entries.
func TestListQueriesUseAnIndex(t *testing.T) {
	s := openTest(t)
	cases := map[string]struct {
		query string
		args  []any
	}{
		"library": {
			`SELECT * FROM entries WHERE status = 'saved' AND deleted_at IS NULL
			AND (? IS NULL OR (finished_on, id) < (?, ?)) ORDER BY finished_on DESC, id DESC LIMIT ?`,
			[]any{nil, nil, nil, 10},
		},
		"drafts": {`SELECT * FROM entries WHERE status = 'draft' AND deleted_at IS NULL
			ORDER BY updated_at DESC, id DESC LIMIT ?`, []any{10}},
		"entries on a work": {`SELECT * FROM entries WHERE work_id = ? AND deleted_at IS NULL
			ORDER BY finished_on DESC, id DESC`, []any{"w1"}},
		"revisions":     {`SELECT * FROM entry_revisions WHERE entry_id = ? ORDER BY revision DESC`, []any{"e1"}},
		"event log":     {`SELECT * FROM session_events WHERE session_id = ? AND seq > ? ORDER BY seq`, []any{"s1", 0}},
		"work by title": {`SELECT * FROM works WHERE medium = ? AND title = ? ORDER BY year DESC, id`, []any{"book", "Dune"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			plan := strings.Join(planFor(t, s, c.query, c.args...), "\n")
			if !strings.Contains(plan, "USING INDEX") && !strings.Contains(plan, "USING COVERING INDEX") {
				t.Fatalf("no index in the plan:\n%s", plan)
			}
			if strings.Contains(plan, "TEMP B-TREE") {
				t.Fatalf("the query sorts in a temporary B-tree:\n%s", plan)
			}
		})
	}
}

func planFor(t *testing.T, s *Store, query string, args ...any) []string {
	t.Helper()
	rows, err := s.readers.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	return out
}

var _ = time.Now

func TestWorksByTitleAndEntriesOnWork(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.SaveWork(ctx, sampleWork("w1")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWork(ctx, sampleWork("w2")); err != nil {
		t.Fatal(err)
	}

	byTitle, titleErr := s.WorksByTitle(ctx, "book", "Dune")
	if titleErr != nil {
		t.Fatal(titleErr)
	}
	if len(byTitle) != 2 {
		t.Fatalf("found %d works, want 2", len(byTitle))
	}

	first := sampleEntry("e1", "w1", 18)
	first.Status = domain.StatusSaved
	first.FinishedOn = "2026-10-01"
	second := sampleEntry("e2", "w1", 20)
	second.Status = domain.StatusSaved
	second.FinishedOn = "2026-10-29"
	for _, e := range []domain.Entry{first, second} {
		if err := s.CreateDraft(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	timeline, timelineErr := s.EntriesOnWork(ctx, "w1")
	if timelineErr != nil {
		t.Fatal(timelineErr)
	}
	if len(timeline) != 2 || timeline[0].ID != "e2" {
		t.Fatalf("timeline = %+v, want newest first", timeline)
	}
	if got, _ := s.EntriesOnWork(ctx, "w2"); len(got) != 0 {
		t.Fatalf("a work with no entries returned %+v", got)
	}
}

func TestSessionReadBackAndActiveList(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.CreateSession(ctx, domain.Session{
		ID: "sess", Kind: domain.SessionGeneral, Title: "what did I think of Dune?",
		State: domain.SessionActive, Messages: []byte(`[{"role":"user"}]`),
	}); err != nil {
		t.Fatal(err)
	}

	got, sessErr := s.Session(ctx, "sess")
	if sessErr != nil {
		t.Fatal(sessErr)
	}
	if got.Kind != domain.SessionGeneral || got.Title != "what did I think of Dune?" {
		t.Fatalf("session = %+v", got)
	}
	if string(got.Messages) != `[{"role":"user"}]` {
		t.Fatalf("messages = %s", got.Messages)
	}

	active, activeErr := s.ActiveSessions(ctx, 10)
	if activeErr != nil {
		t.Fatal(activeErr)
	}
	if len(active) != 1 || active[0].ID != "sess" {
		t.Fatalf("active = %+v", active)
	}
}

// The helpers are small, but they are where an empty string quietly becomes a null, or a null becomes zero.
func TestValueHelpers(t *testing.T) {
	if nullableString("") != nil {
		t.Error("an empty string became a value")
	}
	if got := nullableString("x"); got == nil || *got != "x" {
		t.Error("a value was lost")
	}
	if derefString(nil) != "" {
		t.Error("nil did not become empty")
	}
	if nullableInt(nil) != nil {
		t.Error("nil became a number")
	}
	five := 5
	if got := nullableInt(&five); got == nil || *got != 5 {
		t.Error("a number was lost")
	}
	if intPtr(nil) != nil {
		t.Error("nil became a number")
	}
	var five64 int64 = 5
	if got := intPtr(&five64); got == nil || *got != 5 {
		t.Error("a number was lost")
	}
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Error("boolToInt is wrong")
	}
	for in, want := range map[int]int{-5: 50, 0: 50, 1: 1, 50: 50, 100: 100, 1000: 100} {
		if got := clampLimit(in); got != want {
			t.Errorf("clampLimit(%d) = %d, want %d", in, got, want)
		}
	}
	for _, v := range []any{int64(7), 7, float64(7)} {
		if got := asInt(v); got != 7 {
			t.Errorf("asInt(%v) = %d, want 7", v, got)
		}
	}
	if asInt("nope") != 0 {
		t.Error("asInt accepted a string")
	}
}

// Accepting a source that does not exist is a refusal, not a silent success.
func TestAcceptUnknownSource(t *testing.T) {
	s := openTest(t)
	if err := s.AcceptSource(context.Background(), "nope"); !errors.Is(err, ErrNotAcceptable) {
		t.Fatalf("err = %v, want ErrNotAcceptable", err)
	}
}
