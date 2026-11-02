package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/AutumnsGrove/cairn/internal/domain"
)

// TMDB is the film and TV client, over the v3 API with the key in the query string, the shape Polaris uses.
//
// A TMDB id is only unique within its kind: movie 603 and tv 603 are different works. The provider id stored in
// the database therefore carries the kind, as "movie:603" or "tv:603" (S06 decisions 3, PLAN.md 7).
type TMDB struct {
	client    *client
	coverBase string
	// enabled is false when the config has no key. Lookup and Fetch then answer ReasonOff, which is a normal
	// state the card explains, not a failure of the turn.
	enabled bool
	// images is the image CDN base TMDB reports, kept for a while and replaced by the built-in default when the
	// configuration call fails. A poster failing to load is not a reason to fail a lookup.
	imagesMu   sync.Mutex
	imagesBase string
}

// NewTMDB builds the client. The caller has already checked that a key exists.
func NewTMDB(cfg Config) *TMDB {
	// A caller may build this client directly, so the defaults are applied here as well as in NewFinder.
	cfg = withDefaults(cfg)
	return &TMDB{
		client: &client{
			provider:  ProviderTMDB,
			baseURL:   cfg.TMDBBaseURL,
			keyParam:  "api_key",
			key:       cfg.TMDBKey,
			http:      cfg.HTTPClient,
			limiter:   &limiter{interval: cfg.TMDBCallInterval},
			userAgent: cfg.UserAgent,
		},
		coverBase: cfg.TMDBCoverBase,
		enabled:   strings.TrimSpace(cfg.TMDBKey) != "",
	}
}

// Name is the provider name stored on a source.
func (t *TMDB) Name() string { return ProviderTMDB }

// Mediums are the media TMDB serves.
func (t *TMDB) Mediums() []string { return []string{"film", "tv"} }

// The two search routes, and the details route each kind uses.
const (
	tmdbSearchMovie = "/search/movie"
	tmdbSearchTV    = "/search/tv"
	tmdbDetailsBase = "/"
	tmdbConfigRoute = "/configuration"
)

// searchResponse is the shape both search routes answer with. The two kinds name their fields differently, so
// both pairs are declared and whichever is set wins.
type searchResponse struct {
	Results []struct {
		ID            int    `json:"id"`
		Title         string `json:"title"`
		Name          string `json:"name"`
		OriginalTitle string `json:"original_title"`
		OriginalName  string `json:"original_name"`
		ReleaseDate   string `json:"release_date"`
		FirstAirDate  string `json:"first_air_date"`
		PosterPath    string `json:"poster_path"`
	} `json:"results"`
}

// Lookup searches films or TV. The year is a hint, never a filter: a provider's year disagrees often enough that
// filtering on it would hide the right answer.
func (t *TMDB) Lookup(ctx context.Context, q Query) ([]Candidate, error) {
	if !t.enabled {
		return nil, &Error{Provider: ProviderTMDB, Op: "lookup", Reason: ReasonOff, Err: ErrOff}
	}
	if strings.TrimSpace(q.Title) == "" {
		return nil, &Error{Provider: ProviderTMDB, Op: "lookup", Reason: ReasonMalformed, Err: errors.New("no title")}
	}
	var route string
	params := map[string]string{"query": strings.TrimSpace(q.Title), "include_adult": "false"}
	switch q.Medium {
	case "film":
		route = tmdbSearchMovie
		if q.Year > 0 {
			params["year"] = strconv.Itoa(q.Year)
		}
	case "tv":
		route = tmdbSearchTV
		if q.Year > 0 {
			params["first_air_date_year"] = strconv.Itoa(q.Year)
		}
	default:
		return nil, &Error{
			Provider: ProviderTMDB, Op: "lookup", Reason: ReasonUnsupported,
			Err: fmt.Errorf("tmdb does not serve %q", q.Medium),
		}
	}

	var payload searchResponse
	if _, err := t.client.getJSON(ctx, route, params, &payload); err != nil {
		return nil, err
	}
	return normaliseTMDBSearch(q.Medium, payload, t.coverURL), nil
}

// normaliseTMDBSearch turns a search document into candidates. It is pure, so the fuzzer can drive it with any
// JSON the provider could ever send, and the client never sees a half-built candidate.
func normaliseTMDBSearch(medium string, payload searchResponse, cover func(string) string) []Candidate {
	out := make([]Candidate, 0, len(payload.Results))
	for _, r := range payload.Results {
		title, date, kind := r.Title, r.ReleaseDate, "movie"
		if medium == "tv" {
			title, date, kind = r.Name, r.FirstAirDate, "tv"
		}
		title = cleanTitle(title)
		if r.ID <= 0 || title == "" {
			continue
		}
		out = append(out, Candidate{
			Provider:   ProviderTMDB,
			ProviderID: kind + ":" + strconv.Itoa(r.ID),
			Medium:     medium,
			Title:      title,
			Year:       yearFromDate(date),
			ThumbURL:   cover(r.PosterPath),
		})
	}
	return out
}

// detailResponse is one film or TV document. Every field is optional: a missing one is a fact about the provider's
// answer, not a reason to drop the work.
type detailResponse struct {
	ID            int    `json:"id"`
	IMDBID        string `json:"imdb_id"`
	TVDBID        int    `json:"tvdb_id"`
	Title         string `json:"title"`
	Name          string `json:"name"`
	OriginalTitle string `json:"original_title"`
	OriginalName  string `json:"original_name"`
	ReleaseDate   string `json:"release_date"`
	FirstAirDate  string `json:"first_air_date"`
	Runtime       int    `json:"runtime"`
	Status        string `json:"status"`
	Language      string `json:"original_language"`
	PosterPath    string `json:"poster_path"`
	Genres        []struct {
		Name string `json:"name"`
	} `json:"genres"`
	CreatedBy []struct {
		Name string `json:"name"`
	} `json:"created_by"`
	Credits struct {
		Cast []struct {
			Name string `json:"name"`
		} `json:"cast"`
		Crew []struct {
			Job  string `json:"job"`
			Name string `json:"name"`
		} `json:"crew"`
	} `json:"credits"`
	ExternalIDs struct {
		IMDBID string `json:"imdb_id"`
		TVDBID int    `json:"tvdb_id"`
	} `json:"external_ids"`
	NumberOfSeasons  int `json:"number_of_seasons"`
	NumberOfEpisodes int `json:"number_of_episodes"`
}

// Fetch reads one film or TV work by its kind-prefixed id.
func (t *TMDB) Fetch(ctx context.Context, id string) (Result, error) {
	if !t.enabled {
		return Result{}, &Error{Provider: ProviderTMDB, Op: "fetch", Reason: ReasonOff, Err: ErrOff}
	}
	kind, number, ok := splitTMDBID(id)
	if !ok {
		return Result{}, &Error{
			Provider: ProviderTMDB, Op: "fetch", Reason: ReasonMalformed,
			Err: fmt.Errorf("id %q is not movie:<n> or tv:<n>", id),
		}
	}
	route := tmdbDetailsBase + kind + "/" + number

	var detail detailResponse
	raw, err := t.client.getJSON(ctx, route, map[string]string{"append_to_response": "external_ids,credits"}, &detail)
	if err != nil {
		return Result{}, err
	}

	medium := "film"
	if kind == "tv" {
		medium = "tv"
	}
	source := normaliseTMDBDetail(kind, id, detail, raw)
	merged := domain.Merge(medium, []domain.Source{source})
	return Result{
		Provider:   ProviderTMDB,
		ProviderID: id,
		Medium:     medium,
		Source:     source,
		Work:       merged.Work,
		Conflicts:  merged.Conflicts,
		CoverURL:   t.coverURL(detail.PosterPath),
	}, nil
}

// normaliseTMDBDetail turns one details document into a source. Every field is optional, every string is cleaned
// and bounded, and nothing here can panic: the fuzzer holds that down.
func normaliseTMDBDetail(kind, id string, detail detailResponse, raw []byte) domain.Source {
	title, original, date := detail.Title, detail.OriginalTitle, detail.ReleaseDate
	if kind == "tv" {
		title, original, date = detail.Name, detail.OriginalName, detail.FirstAirDate
	}
	return domain.Source{
		Provider:      ProviderTMDB,
		ProviderID:    id,
		Trust:         domain.TrustVerified,
		Title:         cleanTitle(title),
		OriginalTitle: cleanTitle(original),
		Year:          yearFromDate(date),
		Creators:      tmdbCreators(kind, detail),
		Details:       tmdbDetails(kind, detail),
		CoverSource:   ProviderTMDB,
		Raw:           boundedRaw(raw),
	}
}

// tmdbCreators keeps the people a card shows: who made it, then who is in it. Cast is capped, because a film can
// list a hundred people and the card shows three lines.
func tmdbCreators(kind string, detail detailResponse) []domain.Creator {
	var out []domain.Creator
	seen := map[string]bool{}
	add := func(role, name string) {
		name = cleanTitle(name)
		key := role + "|" + strings.ToLower(name)
		if name == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, domain.Creator{Role: role, Name: name})
	}
	if kind == "tv" {
		for _, person := range detail.CreatedBy {
			add("creator", person.Name)
		}
	} else {
		for _, person := range detail.Credits.Crew {
			if strings.EqualFold(person.Job, "Director") {
				add("director", person.Name)
			}
		}
	}
	for i, person := range detail.Credits.Cast {
		if i == 5 {
			break
		}
		add("actor", person.Name)
	}
	return out
}

// tmdbDetails are the medium-specific facts a work card can show, as plain strings so a new fact needs no
// migration.
func tmdbDetails(kind string, detail detailResponse) map[string]string {
	details := map[string]string{}
	put := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			details[key] = cleanTitle(value)
		}
	}
	var genres []string
	for _, genre := range detail.Genres {
		if name := cleanTitle(genre.Name); name != "" {
			genres = append(genres, name)
		}
	}
	put("genres", strings.Join(genres, ", "))
	put("language", detail.Language)
	if imdb := firstNonEmpty(detail.ExternalIDs.IMDBID, detail.IMDBID); imdb != "" {
		put("imdb", imdb)
	}
	if kind == "tv" {
		put("status", detail.Status)
		put("seasons", strconv.Itoa(detail.NumberOfSeasons))
		put("episodes", strconv.Itoa(detail.NumberOfEpisodes))
		put("first_aired", detail.FirstAirDate)
		if tvdb := firstNonZero(detail.ExternalIDs.TVDBID, detail.TVDBID); tvdb > 0 {
			put("tvdb", strconv.Itoa(tvdb))
		}
	} else {
		put("released", detail.ReleaseDate)
		if detail.Runtime > 0 {
			put("runtime", strconv.Itoa(detail.Runtime))
		}
	}
	// A zero count is not a fact worth showing; remove the empty ones the puts above may have added.
	for _, key := range []string{"seasons", "episodes"} {
		if details[key] == "0" {
			delete(details, key)
		}
	}
	return details
}

// splitTMDBID reads the kind and number out of "movie:603" or "tv:1399".
func splitTMDBID(id string) (string, string, bool) {
	kind, number, ok := strings.Cut(strings.TrimSpace(id), ":")
	if !ok || (kind != "movie" && kind != "tv") {
		return "", "", false
	}
	if n, err := strconv.Atoi(number); err != nil || n <= 0 {
		return "", "", false
	}
	return kind, number, true
}

// coverURL builds a poster URL, reading the image configuration once and falling back to the built-in base.
func (t *TMDB) coverURL(posterPath string) string {
	posterPath = strings.TrimSpace(posterPath)
	if posterPath == "" || !strings.HasPrefix(posterPath, "/") {
		return ""
	}
	return t.imageBase() + posterPath
}

// imageBase returns the cached image CDN prefix. The configuration is read at most once per cache window; a
// failure leaves the built-in default, because a missing poster never fails a lookup.
func (t *TMDB) imageBase() string {
	t.imagesMu.Lock()
	defer t.imagesMu.Unlock()
	if t.imagesBase == "" {
		t.imagesBase = t.coverBase
	}
	return t.imagesBase
}

// Images refreshes the image CDN prefix from TMDB's configuration route. It is optional: the built-in base is
// good enough, and a caller that wants the provider's own answer can ask for it at startup.
func (t *TMDB) Images(ctx context.Context) error {
	var payload struct {
		Images struct {
			SecureBaseURL string   `json:"secure_base_url"`
			PosterSizes   []string `json:"poster_sizes"`
		} `json:"images"`
	}
	if _, err := t.client.getJSON(ctx, tmdbConfigRoute, nil, &payload); err != nil {
		return err
	}
	base := strings.TrimSpace(payload.Images.SecureBaseURL)
	if base == "" || !strings.HasPrefix(base, "https://") {
		return &Error{
			Provider: ProviderTMDB, Op: "images", Reason: ReasonMalformed,
			Err: errors.New("configuration has no secure image base"),
		}
	}
	size := "w342"
	if !contains(payload.Images.PosterSizes, size) && len(payload.Images.PosterSizes) > 0 {
		size = payload.Images.PosterSizes[len(payload.Images.PosterSizes)-1]
	}
	t.imagesMu.Lock()
	t.imagesBase = strings.TrimRight(base, "/") + "/" + size
	t.imagesMu.Unlock()
	return nil
}

// boundedRaw keeps a provider document for the audit trail. It is the provider's own bytes, which is why it is
// stored and never parsed again: the snapshot is evidence, not a second copy of the normalised work.
func boundedRaw(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	// The body is already bounded by the read; copy it so the cached value cannot be mutated by a caller.
	return append(json.RawMessage(nil), raw...)
}

// firstNonZero returns the first positive number, which is how the two copies of an external id are joined.
func firstNonZero(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

// firstNonEmpty returns the first non-blank value.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// contains reports whether a list holds a value.
func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
