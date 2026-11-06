// Package providers turns what TMDB and Open Library say into Sepia's own work types.
//
// Three rules shape everything here (docs/BUILD.md 1, docs/slices/S06-providers.md):
//   - A request carries titles, creators and ids, never review text. Query has no field that could hold an
//     opinion, and nothing else in this package builds a request body.
//   - Provider JSON is untrusted. Every field is optional, every string is bounded and stripped of control
//     characters, and a body over the limit is refused rather than read.
//   - A failure is typed and bounded. Every call has a deadline, a rate limit and a defined error, so the agent
//     can turn any of them into the one lookup_failed card instead of five different ones.
//
// Both clients live in this package rather than in subpackages: they share the query, candidate, result and
// error types, and splitting them would either import in a cycle or need a third package for the contract. The
// seam that makes a client swappable is the interface below, not the directory (S06 decisions 5).
package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/AutumnsGrove/sepia/internal/domain"
)

// The provider names stored on a source. They are the ids the merge and the audit trail use, so they never change.
const (
	ProviderTMDB        = "tmdb"
	ProviderOpenLibrary = "openlibrary"
)

// MaxCandidates is the cap from docs/STANDARDS.md: a lookup returns at best first, and never more than five.
const MaxCandidates = 5

// Reasons a provider call can fail, in the words the UI will use. They are stable: the agent maps each to the
// lookup_failed card and the tests assert them.
const (
	ReasonOff          = "off"                // no key: the provider is disabled, which is a normal state
	ReasonNotFound     = "not_found"          // the provider has never heard of this id
	ReasonRateLimited  = "rate_limited"       // 429, with or without Retry-After
	ReasonTimeout      = "timeout"            // no answer inside the deadline
	ReasonUnavailable  = "unavailable"        // 5xx, a dropped connection, a stalled read
	ReasonMalformed    = "malformed"          // not JSON, the wrong JSON, or a field the wrong type
	ReasonTooLarge     = "too_large"          // the body exceeded the provider limit
	ReasonUnauthorized = "unauthorized"       // the key is wrong or revoked
	ReasonUnsupported  = "unsupported_medium" // no provider serves this medium yet
	ReasonNotAnImage   = "not_an_image"       // a cover that is not an image we can size
	ReasonCoverTooBig  = "cover_too_large"    // a cover over the size or dimension cap
	ReasonCancelled    = "cancelled"          // the caller's context was cancelled
)

// TMDBAttribution is the notice TMDB's API terms require, verbatim, with the bracketed placeholder spelled as
// "application" because that is what Sepia is. It is shown in Settings and on any screen that renders TMDB data
// (G3, docs/handoffs/2026-11-01-G3-tmdb-terms.md); the logo goes beside it, below Sepia's own marks.
const TMDBAttribution = "This application uses TMDB and the TMDB APIs but is not endorsed, certified, or otherwise approved by TMDB."

// ErrOff is returned (wrapped in *Error) when a provider has no key. It is a normal runtime state, not a failure
// of the turn: the lookup card says the metadata service is not set up, and logging still works.
var ErrOff = errors.New("providers: provider not configured")

// Error is one provider failure. It never carries the request URL, because the URL carries the API key.
type Error struct {
	Provider   string
	Op         string
	Reason     string
	Status     int
	RetryAfter time.Duration
	Retryable  bool
	Err        error
}

// Error renders the failure for a log. Nothing here is user-facing: the reason is what the UI maps.
func (e *Error) Error() string {
	msg := fmt.Sprintf("providers: %s %s: %s", e.Provider, e.Op, e.Reason)
	if e.Status != 0 {
		msg += fmt.Sprintf(" (HTTP %d)", e.Status)
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return redactSecrets(msg)
}

// secretParamRE catches a key in a query string. A provider error is a log line, and a log line must never carry
// the key, even when an http client put the whole URL into its own error (docs/BUILD.md invariant 9).
var secretParamRE = regexp.MustCompile(`(?i)(api_key=)[^&\s"']+`)

// redactSecrets replaces a query-string key's value, keeping the parameter name so the error still explains itself.
func redactSecrets(message string) string {
	return secretParamRE.ReplaceAllString(message, "${1}REDACTED")
}

// Unwrap keeps the cause for errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Err }

// Reason returns the stable reason of a failure, or "" when err is not a provider error.
func Reason(err error) string {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Reason
	}
	return ""
}

// IsNotFound reports whether a failure means "no such id".
func IsNotFound(err error) bool { return Reason(err) == ReasonNotFound }

// IsOff reports whether a failure means "this provider has no key".
func IsOff(err error) bool { return errors.Is(err, ErrOff) }

// Query is everything a provider is ever allowed to know about the thing being looked up: a title, a second
// qualifier, a year and the medium. There is deliberately no field for an opinion or a message.
type Query struct {
	Medium  string
	Title   string
	Creator string
	Year    int
}

// String renders a query for a log without the caller's wording beyond the title, which is metadata.
func (q Query) String() string {
	return fmt.Sprintf("%s %q %q %d", q.Medium, q.Title, q.Creator, q.Year)
}

// Candidate is one search result, small enough to draw five of them: what the pick-a-work card needs.
type Candidate struct {
	Provider   string
	ProviderID string
	Medium     string
	Title      string
	Year       int
	Creator    string
	ThumbURL   string
}

// Result is one fetch: the source exactly as it arrived, the work the merge makes of it, and where its cover
// lives remotely. Nothing here is stored yet; the caller keeps the source in work_sources.
type Result struct {
	Provider   string
	ProviderID string
	Medium     string
	Source     domain.Source
	Work       domain.Work
	Conflicts  []domain.Conflict
	// CoverURL is remote. Use Client.Download to keep a copy; nothing stores a URL as a canonical field.
	CoverURL string
}

// Searcher finds candidates for a query. A client implements one per provider.
type Searcher interface {
	Name() string
	// Mediums are the media this provider serves, so the finder can pick a client without trying them all.
	Mediums() []string
	Lookup(ctx context.Context, q Query) ([]Candidate, error)
}

// Fetcher reads one work by its provider id.
type Fetcher interface {
	Name() string
	Fetch(ctx context.Context, id string) (Result, error)
}

// Clock is the injected time source, so a cache and the cover-age rule are testable without sleeping.
type Clock interface {
	Now() time.Time
}

// RealClock is the wall clock.
type RealClock struct{}

// Now returns the current instant.
func (RealClock) Now() time.Time { return time.Now() }

// Finder is the one thing the agent holds: it picks the client for a medium, caches the answer for a few minutes,
// ranks candidates and caps them, and dispatches a fetch by provider name.
type Finder struct {
	searchers []Searcher
	fetchers  map[string]Fetcher
	cache     *cache
	clock     Clock
	dataDir   string
	// covers is the shared plumbing for cover downloads: they go to an image host, not to an API, but the
	// deadline, the user agent and the failure taxonomy are the same.
	covers     *client
	coverHosts map[string]bool
}

// Config wires the clients. Every URL has a default, so a test only overrides the ones the world serves.
type Config struct {
	// TMDBKey enables film and TV lookups. Empty turns them off; it is never an error to start without it.
	TMDBKey string
	// TMDBBaseURL defaults to TMDB's v3 API root.
	TMDBBaseURL string
	// TMDBCoverBase is the image CDN prefix including the size, e.g. https://image.tmdb.org/t/p/w342.
	TMDBCoverBase string
	// OpenLibraryBaseURL defaults to the public site root.
	OpenLibraryBaseURL string
	// OpenLibraryCoverBase defaults to the covers host, without a trailing slash.
	OpenLibraryCoverBase string
	// HTTPClient is the client every call uses. Nil gets a 10 s client.
	HTTPClient HTTPDoer
	// Clock drives the cache TTL and the cover-age rule.
	Clock Clock
	// CacheTTL is how long a lookup or fetch is reused in memory. Zero means the 5-minute default.
	CacheTTL time.Duration
	// DataDir is where covers are stored, under covers/.
	DataDir string
	// UserAgent identifies Sepia to Open Library, which asks for a contactable agent.
	UserAgent string
	// TMDBCallInterval and OpenLibraryCallInterval are the minimum gaps between calls. Zero gets the defaults;
	// a test sets a negative value to disable pacing.
	TMDBCallInterval        time.Duration
	OpenLibraryCallInterval time.Duration
}

// HTTPDoer is the slice of http.Client this package uses, so a test can wrap it without a network.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// NewFinder builds the clients and returns the finder the agent uses. A missing key simply leaves that client out.
func NewFinder(cfg Config) *Finder {
	cfg = withDefaults(cfg)
	f := &Finder{
		fetchers:   map[string]Fetcher{},
		cache:      newCache(cfg.CacheTTL, cfg.Clock),
		clock:      cfg.Clock,
		dataDir:    cfg.DataDir,
		coverHosts: coverHostsFrom(cfg.TMDBCoverBase, cfg.OpenLibraryCoverBase),
		covers: &client{
			provider:  "covers",
			http:      cfg.HTTPClient,
			limiter:   &limiter{interval: cfg.TMDBCallInterval},
			userAgent: cfg.UserAgent,
		},
	}
	// TMDB is built even without a key: it answers ReasonOff, which the card says in plain words ("metadata is
	// not set up") instead of pretending the medium is unsupported.
	tmdb := NewTMDB(cfg)
	f.searchers = append(f.searchers, tmdb)
	f.fetchers[tmdb.Name()] = tmdb

	ol := NewOpenLibrary(cfg)
	f.searchers = append(f.searchers, ol)
	f.fetchers[ol.Name()] = ol
	return f
}

// Lookup finds candidates for a query, ranking them and returning at most five. The zero-medium case is an error,
// not an empty list: "I could not look" and "I found nothing" are different results (docs/STANDARDS.md 3.3).
func (f *Finder) Lookup(ctx context.Context, q Query) ([]Candidate, error) {
	into := q.Medium
	if into == "" {
		return nil, &Error{Op: "lookup", Reason: ReasonUnsupported, Err: errors.New("no medium")}
	}
	// A provider that is off is remembered, not surfaced: another client may serve this medium, and if none
	// does, "off" is the honest answer rather than "unsupported".
	var offErr error
	for _, searcher := range f.searchers {
		if !slices.Contains(searcher.Mediums(), into) {
			continue
		}
		key := "lookup|" + searcher.Name() + "|" + normaliseKey(q)
		if cached, ok := f.cache.get(key); ok {
			// A copy, so a caller filtering or sorting the list cannot change what the next caller sees.
			return append([]Candidate(nil), cached.([]Candidate)...), nil
		}
		candidates, err := searcher.Lookup(ctx, q)
		if err != nil {
			// A provider with no key is off, not broken: another client may still serve this medium.
			if errors.Is(err, ErrOff) {
				offErr = err
				continue
			}
			return nil, err
		}
		ranked := Rank(q, candidates)
		f.cache.put(key, ranked)
		return append([]Candidate(nil), ranked...), nil
	}
	if offErr != nil {
		return nil, offErr
	}
	return nil, &Error{Op: "lookup", Reason: ReasonUnsupported, Err: fmt.Errorf("no provider serves %q", into)}
}

// Fetch reads one work by provider and id.
//
// It is deliberately not cached: a result carries the provider's whole document, and 128 of those is a quarter of
// a gigabyte on the board Sepia runs on. Lookups are what repeat; a fetch of the same work twice in five minutes
// is a second request, bounded by the same rate limit.
func (f *Finder) Fetch(ctx context.Context, provider, id string) (Result, error) {
	fetcher, ok := f.fetchers[provider]
	if !ok {
		return Result{}, &Error{Provider: provider, Op: "fetch", Reason: ReasonUnsupported, Err: errors.New("unknown provider")}
	}
	return fetcher.Fetch(ctx, id)
}

// Rank orders candidates by how well they match the query: exact title first, then similarity, then a year and a
// creator that agree. Ties break by title then id, so two runs give the same order and a reopened card looks the
// same. Duplicate ids are dropped, because a provider can repeat itself across pages.
func Rank(q Query, in []Candidate) []Candidate {
	type rankedCandidate struct {
		candidate Candidate
		score     float64
	}
	normalisedTitle := domain.NormaliseTitle(q.Title)
	seen := make(map[string]bool, len(in))
	ranked := make([]rankedCandidate, 0, len(in))
	for _, c := range in {
		key := c.Provider + "|" + c.ProviderID
		if c.ProviderID == "" || seen[key] {
			continue
		}
		seen[key] = true
		score := domain.TitleSimilarity(strings.TrimSpace(q.Title), strings.TrimSpace(c.Title))
		if normalisedTitle != "" && domain.NormaliseTitle(c.Title) == normalisedTitle {
			score += 1
		}
		if q.Year > 0 && c.Year > 0 {
			if q.Year == c.Year {
				score += 0.25
			} else if diff := q.Year - c.Year; diff < -1 || diff > 1 {
				score -= 0.25
			}
		}
		if q.Creator != "" && c.Creator != "" && strings.Contains(strings.ToLower(c.Creator), strings.ToLower(q.Creator)) {
			score += 0.1
		}
		ranked = append(ranked, rankedCandidate{candidate: c, score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.candidate.Title != b.candidate.Title {
			return a.candidate.Title < b.candidate.Title
		}
		return a.candidate.ProviderID < b.candidate.ProviderID
	})
	out := make([]Candidate, 0, MaxCandidates)
	for _, s := range ranked {
		if len(out) == MaxCandidates {
			break
		}
		out = append(out, s.candidate)
	}
	return out
}

// normaliseKey builds a cache key from the query, lower-cased so "Dune" and "dune" share an entry.
func normaliseKey(q Query) string {
	return fmt.Sprintf("%s|%s|%s|%d", strings.ToLower(q.Medium), strings.ToLower(strings.TrimSpace(q.Title)),
		strings.ToLower(strings.TrimSpace(q.Creator)), q.Year)
}

// withDefaults fills the optional settings, so a test only names what it cares about.
func withDefaults(cfg Config) Config {
	if cfg.TMDBBaseURL == "" {
		cfg.TMDBBaseURL = "https://api.themoviedb.org/3"
	}
	if cfg.TMDBCoverBase == "" {
		cfg.TMDBCoverBase = "https://image.tmdb.org/t/p/w342"
	}
	if cfg.OpenLibraryBaseURL == "" {
		cfg.OpenLibraryBaseURL = "https://openlibrary.org"
	}
	if cfg.OpenLibraryCoverBase == "" {
		cfg.OpenLibraryCoverBase = "https://covers.openlibrary.org"
	}
	if cfg.Clock == nil {
		cfg.Clock = RealClock{}
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = DefaultCacheTTL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: DefaultHTTPTimeout}
	}
	if cfg.TMDBCallInterval == 0 {
		cfg.TMDBCallInterval = TMDBDefaultInterval
	}
	if cfg.OpenLibraryCallInterval == 0 {
		cfg.OpenLibraryCallInterval = OpenLibraryDefaultInterval
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}
	return cfg
}

// The defaults the limits table records (docs/STANDARDS.md 4a).
const (
	// DefaultHTTPTimeout bounds one provider call end to end, including the body read.
	DefaultHTTPTimeout = 10 * time.Second
	// DefaultCacheTTL is how long an in-memory answer is reused.
	DefaultCacheTTL = 5 * time.Minute
	// TMDBDefaultInterval paces TMDB at 10 calls a second, below the ~50/s they allow, so a morning of logging
	// cannot look like a flood.
	TMDBDefaultInterval = 100 * time.Millisecond
	// OpenLibraryDefaultInterval paces Open Library at one call a second, which is what they ask for.
	OpenLibraryDefaultInterval = time.Second
	// DefaultUserAgent identifies Sepia to a provider that asks for contactable clients.
	DefaultUserAgent = "sepia/0.1 (+https://github.com/AutumnsGrove/Sepia)"
)
