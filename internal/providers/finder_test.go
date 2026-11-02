package providers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/store"
	"github.com/AutumnsGrove/cairn/internal/world"
)

// finderRig wires both clients to one world and a finder over them, with pacing off and a movable clock.
type finderRig struct {
	tmdb   *world.Server
	ol     *world.Server
	finder *Finder
	clock  *world.FixedClock
	cfg    Config
}

func newFinderRig(t *testing.T, tweak func(*Config)) *finderRig {
	t.Helper()
	clock := world.NewFixedClock(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	tmdb := world.Start("tmdb", clock)
	ol := world.Start("openlibrary", clock)
	t.Cleanup(tmdb.Close)
	t.Cleanup(ol.Close)
	tmdb.SetBody("/search/movie", []byte(tmdbMovieSearch))
	tmdb.SetBody("/movie/101", []byte(tmdbMovieDetail))
	ol.SetBody("/search.json", []byte(olSearchFixture))
	ol.SetBody("/works/OL1W.json", []byte(olWorkFixture))
	ol.SetBody("/works/OL1W/editions.json", []byte(olEditionsFixture))
	ol.SetBody("/authors/OL9A.json", []byte(olAuthorOne))

	cfg := Config{
		TMDBKey:                 "test-key",
		TMDBBaseURL:             tmdb.URL(),
		TMDBCoverBase:           tmdb.URL(),
		OpenLibraryBaseURL:      ol.URL(),
		OpenLibraryCoverBase:    ol.URL(),
		TMDBCallInterval:        -1,
		OpenLibraryCallInterval: -1,
		DataDir:                 t.TempDir(),
		Clock:                   clock,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	return &finderRig{tmdb: tmdb, ol: ol, finder: NewFinder(cfg), clock: clock, cfg: cfg}
}

func TestFinderDispatchesByMedium(t *testing.T) {
	rig := newFinderRig(t, nil)

	films, err := rig.finder.Lookup(context.Background(), Query{Medium: "film", Title: "The Lantern Keeper"})
	if err != nil {
		t.Fatalf("film lookup: %v", err)
	}
	if len(films) == 0 || films[0].Provider != ProviderTMDB {
		t.Errorf("films = %+v", films)
	}
	if rig.tmdb.Calls("/search/movie") != 1 || rig.ol.Calls("/search.json") != 0 {
		t.Errorf("a film lookup reached the wrong service: tmdb %d, openlibrary %d",
			rig.tmdb.Calls("/search/movie"), rig.ol.Calls("/search.json"))
	}

	books, err := rig.finder.Lookup(context.Background(), Query{Medium: "book", Title: "The Lantern Keeper"})
	if err != nil {
		t.Fatalf("book lookup: %v", err)
	}
	if len(books) == 0 || books[0].Provider != ProviderOpenLibrary {
		t.Errorf("books = %+v", books)
	}
}

func TestFinderOffAndUnsupported(t *testing.T) {
	off := newFinderRig(t, func(cfg *Config) { cfg.TMDBKey = "" })
	_, err := off.finder.Lookup(context.Background(), Query{Medium: "film", Title: "x"})
	if !IsOff(err) {
		t.Fatalf("film lookup without a key = %v, want off", err)
	}
	if got := Reason(err); got != ReasonOff {
		t.Errorf("reason = %q", got)
	}
	// Books do not need TMDB, so they keep working with no key at all.
	if _, err := off.finder.Lookup(context.Background(), Query{Medium: "book", Title: "The Lantern Keeper"}); err != nil {
		t.Errorf("book lookup without a key = %v", err)
	}

	full := newFinderRig(t, nil)
	if _, err := full.finder.Lookup(context.Background(), Query{Medium: "album", Title: "x"}); Reason(err) != ReasonUnsupported {
		t.Errorf("album lookup = %v, want unsupported", err)
	}
	if _, err := full.finder.Lookup(context.Background(), Query{Medium: "", Title: "x"}); Reason(err) != ReasonUnsupported {
		t.Errorf("empty medium = %v, want unsupported", err)
	}
	if _, err := full.finder.Fetch(context.Background(), "nope", "1"); Reason(err) != ReasonUnsupported {
		t.Errorf("unknown provider = %v, want unsupported", err)
	}
}

// A cache that outlives the TTL, or never expires, is a stale card. This pins both halves: reused inside the
// window, fetched again after it.
func TestFinderCachesForTheWindow(t *testing.T) {
	rig := newFinderRig(t, func(cfg *Config) { cfg.CacheTTL = time.Minute })
	query := Query{Medium: "film", Title: "The Lantern Keeper"}
	for i := 0; i < 3; i++ {
		if _, err := rig.finder.Lookup(context.Background(), query); err != nil {
			t.Fatalf("lookup %d: %v", i+1, err)
		}
	}
	if got := rig.tmdb.Calls("/search/movie"); got != 1 {
		t.Errorf("%d calls inside the window, want 1", got)
	}
	rig.clock.Advance(2 * time.Minute)
	if _, err := rig.finder.Lookup(context.Background(), query); err != nil {
		t.Fatalf("lookup after the window: %v", err)
	}
	if got := rig.tmdb.Calls("/search/movie"); got != 2 {
		t.Errorf("%d calls after the window, want 2", got)
	}

	// A fetch is not cached: its result carries the provider's whole document, and a quarter of a gigabyte of
	// those would not fit on the board. Two fetches are two requests.
	for i := 0; i < 2; i++ {
		if _, err := rig.finder.Fetch(context.Background(), ProviderTMDB, "movie:101"); err != nil {
			t.Fatalf("fetch %d: %v", i+1, err)
		}
	}
	if got := rig.tmdb.Calls("/movie/101"); got != 2 {
		t.Errorf("%d fetches, want 2", got)
	}
}

func TestCacheIsBounded(t *testing.T) {
	c := newCache(time.Minute, world.NewFixedClock(time.Now()))
	for i := 0; i < maxCacheEntries*2; i++ {
		c.put(strings.Repeat("k", i%32)+string(rune('a'+i%26)), i)
	}
	if len(c.entries) > maxCacheEntries {
		t.Errorf("cache holds %d entries, want at most %d", len(c.entries), maxCacheEntries)
	}
	if _, ok := c.get("never-stored"); ok {
		t.Error("cache answered for a key it never had")
	}
}

func TestRank(t *testing.T) {
	query := Query{Medium: "film", Title: "The Lantern Keeper", Year: 2019, Creator: "Ada Voss"}
	candidates := []Candidate{
		{Provider: "tmdb", ProviderID: "movie:3", Title: "Lantern Keepers", Year: 2021},
		{Provider: "tmdb", ProviderID: "movie:1", Title: "The Lantern Keeper", Year: 2019, Creator: "Ada Voss"},
		{Provider: "tmdb", ProviderID: "movie:1", Title: "The Lantern Keeper", Year: 2019}, // a duplicate id
		{Provider: "tmdb", ProviderID: "movie:2", Title: "The Lantern Keeper", Year: 1999},
		{Provider: "tmdb", ProviderID: "", Title: "No id"},
	}
	ranked := Rank(query, candidates)
	if len(ranked) != 3 {
		t.Fatalf("ranked = %+v, want three distinct ids", ranked)
	}
	if ranked[0].ProviderID != "movie:1" || ranked[0].Year != 2019 {
		t.Errorf("first = %+v, want the exact year and title, and the creator match", ranked[0])
	}
	if ranked[1].ProviderID != "movie:2" {
		t.Errorf("second = %+v, want the same title with the wrong year second", ranked[1])
	}
	if ranked[2].ProviderID != "movie:3" {
		t.Errorf("third = %+v", ranked[2])
	}
}

func TestRankCapsAtFive(t *testing.T) {
	var candidates []Candidate
	for i := 0; i < 9; i++ {
		candidates = append(candidates, Candidate{Provider: "tmdb", ProviderID: string(rune('a' + i)), Title: "Same"})
	}
	if got := len(Rank(Query{Title: "Same"}, candidates)); got != MaxCandidates {
		t.Errorf("%d candidates, want %d", got, MaxCandidates)
	}
}

// Two providers that disagree about a year keep the earliest and record the disagreement, which is what the
// "why" sheet reads. This is the cross-source half of S06.7.
func TestSourcesThatDisagreeKeepTheConflict(t *testing.T) {
	merged := domain.Merge("film", []domain.Source{
		{Provider: "tmdb", ProviderID: "movie:101", Trust: domain.TrustVerified, Title: "The Lantern Keeper", Year: 2019},
		{Provider: "web", ProviderID: "w1", Trust: domain.TrustVerified, Title: "The Lantern Keeper", Year: 1999},
	})
	if merged.Work.Year != 1999 {
		t.Errorf("year = %d, want the earliest", merged.Work.Year)
	}
	if len(merged.Conflicts) == 0 {
		t.Fatal("the disagreement was hidden")
	}
	if merged.Conflicts[0].Field != "year" {
		t.Errorf("conflict = %+v", merged.Conflicts[0])
	}
}

// A provider id is the merge's join key, so a re-fetch must produce the same work. If it did not, every retry
// would look like a new work and the library would fill with duplicates.
func TestRefetchIsTheSameWork(t *testing.T) {
	rig := newFinderRig(t, func(cfg *Config) { cfg.CacheTTL = time.Nanosecond })
	near := rig.finder
	near.cache = newCache(time.Nanosecond, rig.clock)

	for _, tc := range []struct{ provider, id string }{
		{ProviderTMDB, "movie:101"},
		{ProviderOpenLibrary, "OL1W"},
	} {
		first, err := near.Fetch(context.Background(), tc.provider, tc.id)
		if err != nil {
			t.Fatalf("first fetch %s: %v", tc.id, err)
		}
		rig.clock.Advance(time.Millisecond)
		second, err := near.Fetch(context.Background(), tc.provider, tc.id)
		if err != nil {
			t.Fatalf("second fetch %s: %v", tc.id, err)
		}
		if !reflect.DeepEqual(first.Work, second.Work) {
			t.Errorf("%s re-fetch changed the work:\n%+v\n%+v", tc.id, first.Work, second.Work)
		}
		if !reflect.DeepEqual(first.Source.Raw, second.Source.Raw) {
			t.Errorf("%s re-fetch changed the snapshot", tc.id)
		}
	}
}

// The happy route end to end at this boundary: a fetch, then the snapshot in the database with the trust an API
// earns. The store is imported by a test only; shipped code in this package never reaches for it.
func TestFetchThenStoreTheSnapshot(t *testing.T) {
	rig := newFinderRig(t, nil)
	ctx := context.Background()

	result, err := rig.finder.Fetch(ctx, ProviderTMDB, "movie:101")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !result.Work.Verified {
		t.Fatal("a TMDB work is not verified")
	}

	db, err := store.Open(ctx, store.Options{
		Path:  filepath.Join(t.TempDir(), "cairn.db"),
		Now:   rig.clock.Now,
		NewID: world.NewSeqIDs("w").New,
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if migrateErr := db.Migrate(ctx); migrateErr != nil {
		t.Fatalf("Migrate: %v", migrateErr)
	}

	work := result.Work
	work.ID = "w1"
	if saveErr := db.SaveWork(ctx, work); saveErr != nil {
		t.Fatalf("SaveWork: %v", saveErr)
	}
	if _, addErr := db.AddSource(ctx, work.ID, result.Source); addErr != nil {
		t.Fatalf("AddSource: %v", addErr)
	}
	sources, err := db.Sources(ctx, work.ID)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %+v", sources)
	}
	if sources[0].Trust != domain.TrustVerified || sources[0].Provider != ProviderTMDB {
		t.Errorf("source = %+v", sources[0])
	}
	var document map[string]any
	if err := json.Unmarshal(sources[0].Raw, &document); err != nil {
		t.Fatalf("stored snapshot is not JSON: %v", err)
	}
	if document["title"] != "The Lantern Keeper" {
		t.Errorf("stored snapshot = %s", sources[0].Raw)
	}
}

// FuzzTMDBPayload proves any JSON a compromised or broken provider could send leaves the normalisers calm.
func FuzzTMDBPayload(f *testing.F) {
	f.Add([]byte(tmdbMovieSearch), []byte(tmdbMovieDetail), "film")
	f.Add([]byte(`{"results":null}`), []byte(`{}`), "tv")
	f.Add([]byte(`[]`), []byte(`null`), "")
	f.Add([]byte(`{"results":[{"id":-1,"title":"\u0000"}]}`), []byte(`{"id":1}`), "movie")
	f.Fuzz(func(t *testing.T, search, detail []byte, medium string) {
		var s searchResponse
		_ = json.Unmarshal(search, &s)
		_ = normaliseTMDBSearch(medium, s, func(string) string { return "cover" })

		var d detailResponse
		_ = json.Unmarshal(detail, &d)
		for _, kind := range []string{"movie", "tv", "weird"} {
			source := normaliseTMDBDetail(kind, "movie:1", d, detail)
			if len(source.Title) > maxTitleChars {
				t.Fatalf("title is %d characters", len(source.Title))
			}
		}
	})
}

// FuzzOpenLibraryPayload does the same for the book normalisers.
func FuzzOpenLibraryPayload(f *testing.F) {
	f.Add([]byte(olSearchFixture), []byte(olWorkFixture), []byte(olEditionsFixture))
	f.Add([]byte(`{}`), []byte(`{}`), []byte(`{}`))
	f.Add([]byte(`{"docs":null}`), []byte(`null`), []byte(`null`))
	f.Add([]byte(`{"docs":[{"key":"/works/","title":null}]}`), []byte(`{"covers":null}`), []byte(`{"entries":[{}]}`))
	f.Fuzz(func(t *testing.T, search, work, editions []byte) {
		var s olSearchResponse
		_ = json.Unmarshal(search, &s)
		_ = normaliseOLSearch(s, func(int, string) string { return "cover" })

		var w olWork
		_ = json.Unmarshal(work, &w)
		var e olEditions
		_ = json.Unmarshal(editions, &e)
		source := normaliseOLWork("OL1W", w, []string{"Ada Voss"}, e, olEnvelope(work, editions))
		if len(source.Title) > maxTitleChars {
			t.Fatalf("title is %d characters", len(source.Title))
		}
	})
}

// The attribution is a term of use, not a preference: this test fails if the words drift, and S11 places them.
func TestTMDBAttributionIsTheRequiredNotice(t *testing.T) {
	want := "This application uses TMDB and the TMDB APIs but is not endorsed, certified, or otherwise approved by TMDB."
	if TMDBAttribution != want {
		t.Errorf("TMDBAttribution = %q, want the exact words the terms require", TMDBAttribution)
	}
}
