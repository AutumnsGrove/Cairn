package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/AutumnsGrove/cairn/internal/store/db"
)

// A smoke test over the generated queries: it proves the SQL, the schema and the generated Go agree, which is what
// the drift job cannot check on its own. The full repository layer and the EXPLAIN QUERY PLAN tests come next.

func TestGeneratedQueriesAgainstTheRealSchema(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	const at = "2026-10-31T00:00:00Z"

	if err := s.InTx(ctx, func(q *db.Queries) error {
		if err := q.CreateWork(ctx, db.CreateWorkParams{
			ID: "w1", Medium: "book", Title: "Dune", OriginalTitle: "",
			Year: 1965, CreatorJson: `[{"role":"author","name":"Frank Herbert"}]`,
			DetailsJson: "{}", CoverPath: "", CoverSource: "",
			PrimaryProvider: "openlibrary", PrimaryID: "OL893415W",
			Verified: 1, CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			return err
		}
		if err := q.CreateWorkSource(ctx, db.CreateWorkSourceParams{
			ID: "s1", WorkID: "w1", Provider: "openlibrary", ProviderID: "OL893415W",
			RawJson: "{}", Trust: "verified", CitationsJson: "[]",
			PolarisThreadID: "", FetchedAt: at,
		}); err != nil {
			return err
		}
		return q.CreateEntry(ctx, db.CreateEntryParams{
			ID: "e1", WorkID: "w1", RatingQ: 18, RatingScale: "quarter_stars_20",
			Favourite: 0, Verdict: "would read again", Body: "the world felt so lived in",
			BodyOrigin: "extracted", BodySpansJson: "[]", Format: "paperback",
			FinishedOn: ptr("2026-10-29"), Status: "saved", CreatedAt: at, UpdatedAt: at,
		})
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	work, err := s.ReadQueries().GetWork(ctx, "w1")
	if err != nil {
		t.Fatalf("GetWork: %v", err)
	}
	if work.Title != "Dune" || work.Year != 1965 {
		t.Fatalf("work = %+v", work)
	}

	// The first page: no cursor. The second page passes the cursor's finished_on and id.
	entries, err := s.ReadQueries().ListLibrary(ctx, db.ListLibraryParams{
		Column1: nil, FinishedOn: nil, FinishedOn_2: nil, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListLibrary: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "e1" {
		t.Fatalf("library = %+v", entries)
	}

	// A draft is not in the library: the query's status filter is the rule, not the caller's good manners.
	if draftErr := s.InTx(ctx, func(q *db.Queries) error {
		return q.CreateEntry(ctx, db.CreateEntryParams{
			ID: "e2", WorkID: "w1", RatingQ: 18, RatingScale: "quarter_stars_20",
			Body: "a draft", BodyOrigin: "typed", Status: "draft", CreatedAt: at, UpdatedAt: at,
		})
	}); draftErr != nil {
		t.Fatal(draftErr)
	}
	entries, err = s.ReadQueries().ListLibrary(ctx, db.ListLibraryParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the library returned a draft: %+v", entries)
	}
	drafts, err := s.ReadQueries().ListDrafts(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].ID != "e2" {
		t.Fatalf("drafts = %+v", drafts)
	}
}

// The event log's sequence number is chosen by the statement, so concurrent appenders cannot collide.
func TestEventLogSequenceIsStrictlyIncreasing(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.InTx(ctx, func(q *db.Queries) error {
		return q.CreateSession(ctx, db.CreateSessionParams{
			ID: "sess", Kind: "entry", Title: "", MessagesJson: "[]", State: "active",
			CreatedAt: "2026-10-31T00:00:00Z", UpdatedAt: "2026-10-31T00:00:00Z",
		})
	}); err != nil {
		t.Fatal(err)
	}

	const n = 20
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			done <- s.InTx(ctx, func(q *db.Queries) error {
				return q.AppendSessionEvent(ctx, db.AppendSessionEventParams{
					ID:          "ev" + itoa(i),
					SessionID:   "sess",
					SessionID_2: "sess",
					Kind:        "text_delta",
					PayloadJson: "{}",
					CreatedAt:   "2026-10-31T00:00:00Z",
				})
			})
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	events, err := s.ReadQueries().ListSessionEventsSince(ctx, db.ListSessionEventsSinceParams{
		SessionID: "sess", Seq: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != n {
		t.Fatalf("%d events, want %d", len(events), n)
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
	}
}

func ptr(s string) *string { return &s }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

var _ = sql.ErrNoRows
