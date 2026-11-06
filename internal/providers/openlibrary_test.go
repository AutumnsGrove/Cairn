package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/sepia/internal/world"
)

// The book fixtures are invented too: no real title, author or publisher appears in the repository.

const olSearchFixture = `{"docs": [
  {"key": "/works/OL1W", "title": "The Lantern Keeper", "author_name": ["Ada Voss"],
   "first_publish_year": 1999, "cover_i": 123, "edition_key": ["OL1M"]},
  {"key": "/works/OL2W", "title": "Lantern Keepers at Sea", "author_name": ["Tomas Reed"],
   "first_publish_year": 2004}
]}`

const olWorkFixture = `{
  "key": "/works/OL1W", "title": "The Lantern Keeper", "first_publish_date": "1999",
  "covers": [123], "subjects": ["Lighthouses", "Memory"],
  "authors": [{"author": {"key": "/authors/OL9A"}}, {"author": {"key": "/authors/OL8A"}}]
}`

const olEditionsFixture = `{"entries": [
  {"title": "The Lantern Keeper", "publish_date": "1999", "publishers": ["Northlight Press"],
   "number_of_pages": 312, "isbn_13": ["9780000000001"], "series": ["Lantern"], "covers": [123]},
  {"title": "The Lantern Keeper (reprint)", "publish_date": "2003", "number_of_pages": 320}
]}`

const (
	olAuthorOne = `{"name": "Ada Voss"}`
	olAuthorTwo = `{"name": "Tomas Reed"}`
)

type olRig struct {
	srv    *world.Server
	client *OpenLibrary
	cfg    Config
}

func newOLRig(t *testing.T, tweak func(*Config, *world.Service)) *olRig {
	t.Helper()
	clock := world.NewFixedClock(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	srv := world.Start("openlibrary", clock)
	t.Cleanup(srv.Close)
	srv.SetBody("/search.json", []byte(olSearchFixture))
	srv.SetBody("/works/OL1W.json", []byte(olWorkFixture))
	srv.SetBody("/works/OL1W/editions.json", []byte(olEditionsFixture))
	srv.SetBody("/authors/OL9A.json", []byte(olAuthorOne))
	srv.SetBody("/authors/OL8A.json", []byte(olAuthorTwo))

	cfg := Config{
		OpenLibraryBaseURL:      srv.URL(),
		OpenLibraryCoverBase:    srv.URL() + "/covers",
		OpenLibraryCallInterval: -1,
		TMDBCallInterval:        -1,
		DataDir:                 t.TempDir(),
		Clock:                   clock,
	}
	if tweak != nil {
		tweak(&cfg, srv.Service)
	}
	return &olRig{srv: srv, client: NewOpenLibrary(cfg), cfg: cfg}
}

// --- happy ----------------------------------------------------------------------------------------------

func TestOpenLibraryLookup(t *testing.T) {
	rig := newOLRig(t, nil)
	candidates, err := rig.client.Lookup(context.Background(), Query{Medium: "book", Title: "The Lantern Keeper"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %+v", candidates)
	}
	first := candidates[0]
	if first.ProviderID != "OL1W" || first.Year != 1999 || first.Creator != "Ada Voss" || first.Medium != "book" {
		t.Errorf("first = %+v", first)
	}
	if !strings.HasSuffix(first.ThumbURL, "/covers/b/id/123-M.jpg") {
		t.Errorf("thumb = %q", first.ThumbURL)
	}
	// The second has no cover, which must be an empty URL and not a broken link.
	if candidates[1].ThumbURL != "" {
		t.Errorf("coverless candidate = %+v", candidates[1])
	}
}

func TestOpenLibraryFetch(t *testing.T) {
	rig := newOLRig(t, nil)
	result, err := rig.client.Fetch(context.Background(), "OL1W")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Work.Title != "The Lantern Keeper" || result.Work.Year != 1999 || result.Work.Medium != "book" {
		t.Errorf("work = %+v", result.Work)
	}
	if len(result.Work.Creators) != 2 || result.Work.Creators[0].Name != "Ada Voss" || result.Work.Creators[0].Role != "author" {
		t.Errorf("creators = %+v", result.Work.Creators)
	}
	for key, want := range map[string]string{
		"publisher": "Northlight Press", "pages": "312", "isbn_13": "9780000000001",
		"series": "Lantern", "first_published": "1999", "edition_year": "1999",
	} {
		if got := result.Source.Details[key]; got != want {
			t.Errorf("details[%q] = %q, want %q", key, got, want)
		}
	}
	if result.CoverURL != rig.srv.URL()+"/covers/b/id/123-L.jpg" {
		t.Errorf("cover = %q", result.CoverURL)
	}

	// The snapshot holds the work and the editions together, so an audit can see what the source said.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(result.Source.Raw, &envelope); err != nil {
		t.Fatalf("raw envelope: %v", err)
	}
	if len(envelope["work"]) == 0 || len(envelope["editions"]) == 0 {
		t.Errorf("raw envelope = %s", result.Source.Raw)
	}
}

// A work id is accepted in the form the search result gave it, which includes the route prefix.
func TestOpenLibraryAcceptsARouteShapedID(t *testing.T) {
	rig := newOLRig(t, nil)
	result, err := rig.client.Fetch(context.Background(), "/works/OL1W")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.ProviderID != "OL1W" {
		t.Errorf("provider id = %q", result.ProviderID)
	}
}

// --- bad ------------------------------------------------------------------------------------------------

// A missing author or edition is a thinner work, not a failed fetch: the title and the year are what the card
// needs most, and Open Library is allowed to be incomplete.
func TestOpenLibraryPartialWork(t *testing.T) {
	rig := newOLRig(t, func(cfg *Config, srv *world.Service) {
		srv.SetFault("/authors/OL9A.json", world.FaultNotFound)
		srv.SetFault("/works/OL1W/editions.json", world.FaultError)
	})
	result, err := rig.client.Fetch(context.Background(), "OL1W")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Work.Title != "The Lantern Keeper" || result.Work.Year != 1999 {
		t.Errorf("work = %+v", result.Work)
	}
	if len(result.Work.Creators) != 1 || result.Work.Creators[0].Name != "Tomas Reed" {
		t.Errorf("creators = %+v, want the one author that answered", result.Work.Creators)
	}
	if result.Source.Details["publisher"] != "" {
		t.Errorf("details = %+v, want no edition facts", result.Source.Details)
	}
}

func TestOpenLibraryRejectsBadIDs(t *testing.T) {
	rig := newOLRig(t, nil)
	for _, id := range []string{"", "  ", "../works/OL1W", "OL1W?x=1", "OL1W/x"} {
		if _, err := rig.client.Fetch(context.Background(), id); Reason(err) != ReasonMalformed {
			t.Errorf("Fetch(%q) = %v, want malformed", id, err)
		}
	}
}

func TestOpenLibraryMissingFields(t *testing.T) {
	rig := newOLRig(t, func(cfg *Config, srv *world.Service) {
		srv.SetBody("/search.json", []byte(`{"docs": [
			{"key": "", "title": "no id"},
			{"key": "/works/OL5W", "title": null},
			{"key": "/works/OL6W", "title": "Kept"}
		]}`))
	})
	candidates, err := rig.client.Lookup(context.Background(), Query{Medium: "book", Title: "Kept"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ProviderID != "OL6W" {
		t.Fatalf("candidates = %+v", candidates)
	}
	if candidates[0].Year != 0 || candidates[0].Creator != "" {
		t.Errorf("candidate = %+v", candidates[0])
	}
}

func TestOpenLibraryWrongMedium(t *testing.T) {
	rig := newOLRig(t, nil)
	if _, err := rig.client.Lookup(context.Background(), Query{Medium: "film", Title: "x"}); Reason(err) != ReasonUnsupported {
		t.Errorf("Lookup(film) = %v, want unsupported", err)
	}
}

// --- sad ------------------------------------------------------------------------------------------------

func TestOpenLibraryFailures(t *testing.T) {
	cases := map[string]struct {
		fault   world.Fault
		timeout time.Duration
		reason  string
	}{
		"outage":       {fault: world.FaultError, reason: ReasonUnavailable},
		"rate limit":   {fault: world.FaultRateLimit, reason: ReasonRateLimited},
		"timeout":      {fault: world.FaultSlow, timeout: 40 * time.Millisecond, reason: ReasonTimeout},
		"stalled read": {fault: world.FaultStall, timeout: 60 * time.Millisecond, reason: ReasonTimeout},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newOLRig(t, func(cfg *Config, srv *world.Service) {
				srv.SetFault("/search.json", tc.fault)
				if tc.timeout > 0 {
					cfg.HTTPClient = &http.Client{Timeout: tc.timeout}
				}
			})
			for i := 0; i < 2; i++ {
				_, err := rig.client.Lookup(context.Background(), Query{Medium: "book", Title: "x"})
				if Reason(err) != tc.reason {
					t.Fatalf("call %d = %v, want %s", i+1, err, tc.reason)
				}
			}
		})
	}
}

func TestOpenLibraryCancelled(t *testing.T) {
	rig := newOLRig(t, func(cfg *Config, srv *world.Service) { srv.SetFault("/search.json", world.FaultSlow) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rig.client.Lookup(ctx, Query{Medium: "book", Title: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lookup = %v", err)
	}
}

// The privacy rule at this boundary: a book lookup sends a title, an author and the paging fields. Nothing here
// can carry an opinion, and this test fails if something tries.
func TestOpenLibraryRequestCarriesOnlyMetadata(t *testing.T) {
	rig := newOLRig(t, nil)
	if _, err := rig.client.Lookup(context.Background(), Query{Medium: "book", Title: "The Lantern Keeper", Creator: "Ada Voss"}); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	values, err := url.ParseQuery(rig.srv.Requests()[0].Query)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	allowed := map[string]bool{"title": true, "author": true, "limit": true, "fields": true}
	for key := range values {
		if !allowed[key] {
			t.Errorf("request sent %q, which is not metadata", key)
		}
	}
	if values.Get("title") != "The Lantern Keeper" || values.Get("author") != "Ada Voss" {
		t.Errorf("query = %q", rig.srv.Requests()[0].Query)
	}
}

// The user agent names Sepia and a contact, which is what Open Library asks for.
func TestOpenLibraryUserAgent(t *testing.T) {
	rig := newOLRig(t, nil)
	if _, err := rig.client.Lookup(context.Background(), Query{Medium: "book", Title: "x"}); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	agent := rig.srv.Requests()[0].Header.Get("User-Agent")
	if !strings.Contains(agent, "sepia") || !strings.Contains(agent, "github.com/AutumnsGrove/Sepia") {
		t.Errorf("User-Agent = %q", agent)
	}
}
