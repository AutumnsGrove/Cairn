package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/AutumnsGrove/cairn/internal/domain"
)

// OpenLibrary is the book client. A work is the abstract book, not an edition: a translation or a reprint is the
// same work, and the edition actually read lives in details (PLAN.md 7).
type OpenLibrary struct {
	client    *client
	coverBase string
}

// The routes the client uses.
const (
	olSearchRoute    = "/search.json"
	olWorksRoute     = "/works/"
	olAuthorsRoute   = "/authors/"
	olEditionsSuffix = "/editions.json"
)

// OpenLibrary asks for a modest search page and no more than a few edition rows; both are capped so one lookup
// cannot pull a book's whole history.
const (
	olSearchLimit  = "5"
	olEditionLimit = "20"
	olMaxAuthors   = 2
)

// NewOpenLibrary builds the client. Open Library needs no key, only a polite user agent and pacing.
func NewOpenLibrary(cfg Config) *OpenLibrary {
	cfg = withDefaults(cfg)
	return &OpenLibrary{
		client: &client{
			provider:  ProviderOpenLibrary,
			baseURL:   cfg.OpenLibraryBaseURL,
			http:      cfg.HTTPClient,
			limiter:   &limiter{interval: cfg.OpenLibraryCallInterval},
			userAgent: cfg.UserAgent,
		},
		coverBase: cfg.OpenLibraryCoverBase,
	}
}

// Name is the provider name stored on a source.
func (o *OpenLibrary) Name() string { return ProviderOpenLibrary }

// Mediums are the media Open Library serves.
func (o *OpenLibrary) Mediums() []string { return []string{"book"} }

// olSearchResponse is the search route's answer. Only the fields the card needs are read.
type olSearchResponse struct {
	Docs []struct {
		Key              string   `json:"key"`
		Title            string   `json:"title"`
		AuthorName       []string `json:"author_name"`
		FirstPublishYear int      `json:"first_publish_year"`
		CoverID          int      `json:"cover_i"`
		EditionKey       []string `json:"edition_key"`
	} `json:"docs"`
}

// Lookup searches works by title and an optional author.
func (o *OpenLibrary) Lookup(ctx context.Context, q Query) ([]Candidate, error) {
	if strings.TrimSpace(q.Title) == "" {
		return nil, &Error{Provider: ProviderOpenLibrary, Op: "lookup", Reason: ReasonMalformed, Err: errors.New("no title")}
	}
	if q.Medium != "" && q.Medium != "book" {
		return nil, &Error{
			Provider: ProviderOpenLibrary, Op: "lookup", Reason: ReasonUnsupported,
			Err: fmt.Errorf("open library does not serve %q", q.Medium),
		}
	}

	var payload olSearchResponse
	_, err := o.client.getJSON(ctx, olSearchRoute, map[string]string{
		"title":  strings.TrimSpace(q.Title),
		"author": strings.TrimSpace(q.Creator),
		"limit":  olSearchLimit,
		"fields": "key,title,author_name,first_publish_year,cover_i,edition_key",
	}, &payload)
	if err != nil {
		return nil, err
	}

	return normaliseOLSearch(payload, o.coverURL), nil
}

// normaliseOLSearch turns a search document into candidates. It is pure, so the fuzzer drives it with anything a
// provider could send, and the client only ever sees finished candidates.
func normaliseOLSearch(payload olSearchResponse, cover func(int, string) string) []Candidate {
	out := make([]Candidate, 0, len(payload.Docs))
	for _, doc := range payload.Docs {
		id := strings.TrimPrefix(strings.TrimSpace(doc.Key), "/works/")
		title := cleanTitle(doc.Title)
		if id == "" || title == "" {
			continue
		}
		out = append(out, Candidate{
			Provider:   ProviderOpenLibrary,
			ProviderID: id,
			Medium:     "book",
			Title:      title,
			Year:       doc.FirstPublishYear,
			Creator:    cleanTitle(strings.Join(firstN(doc.AuthorName, olMaxAuthors), ", ")),
			ThumbURL:   cover(doc.CoverID, "M"),
		})
	}
	return out
}

// olWork is the work document: the abstract book.
type olWork struct {
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	FirstPublishDate string   `json:"first_publish_date"`
	Covers           []int    `json:"covers"`
	Subjects         []string `json:"subjects"`
	Authors          []struct {
		Author struct {
			Key string `json:"key"`
		} `json:"author"`
	} `json:"authors"`
}

// olAuthor is the one field author lookups need.
type olAuthor struct {
	Name string `json:"name"`
}

// olEditions is the editions list. The edition read goes in details, so only the fields a card shows are read.
type olEditions struct {
	Entries []struct {
		Title       string   `json:"title"`
		PublishDate string   `json:"publish_date"`
		Publishers  []string `json:"publishers"`
		Pages       int      `json:"number_of_pages"`
		ISBN10      []string `json:"isbn_10"`
		ISBN13      []string `json:"isbn_13"`
		Series      []string `json:"series"`
		Covers      []int    `json:"covers"`
	} `json:"entries"`
}

// Fetch reads one work, its first author names and its editions. Author and edition lookups are best effort: the
// work itself is the answer, and a missing name is not a reason to fail it.
func (o *OpenLibrary) Fetch(ctx context.Context, id string) (Result, error) {
	id = strings.TrimPrefix(strings.TrimSpace(id), "/works/")
	if id == "" || strings.ContainsAny(id, "/?#") {
		return Result{}, &Error{
			Provider: ProviderOpenLibrary, Op: "fetch", Reason: ReasonMalformed,
			Err: fmt.Errorf("id %q is not a work id", id),
		}
	}

	var work olWork
	rawWork, err := o.client.getJSON(ctx, olWorksRoute+id+".json", nil, &work)
	if err != nil {
		return Result{}, err
	}

	authors := o.authorNames(ctx, work.Authors)
	var editions olEditions
	rawEditions, editionErr := o.client.getJSON(ctx, olWorksRoute+id+olEditionsSuffix, map[string]string{"limit": olEditionLimit}, &editions)
	if editionErr != nil && ctx.Err() != nil {
		// Only a cancelled caller aborts the fetch; a failed editions list is a thinner work, not a failure.
		return Result{}, editionErr
	}

	coverID := 0
	if len(work.Covers) > 0 {
		coverID = work.Covers[0]
	}
	source := normaliseOLWork(id, work, authors, editions, olEnvelope(rawWork, rawEditions))
	merged := domain.Merge("book", []domain.Source{source})
	return Result{
		Provider:   ProviderOpenLibrary,
		ProviderID: id,
		Medium:     "book",
		Source:     source,
		Work:       merged.Work,
		Conflicts:  merged.Conflicts,
		CoverURL:   o.coverURL(coverID, "L"),
	}, nil
}

// authorNames reads the first few author names. A lookup that fails is skipped: the work still has a title, a
// year and an edition, which is what the card needs most.
func (o *OpenLibrary) authorNames(ctx context.Context, refs []struct {
	Author struct {
		Key string `json:"key"`
	} `json:"author"`
},
) []string {
	var out []string
	for i, ref := range refs {
		if i == olMaxAuthors || ctx.Err() != nil {
			break
		}
		authorID := strings.TrimPrefix(strings.TrimSpace(ref.Author.Key), "/authors/")
		if authorID == "" || strings.ContainsAny(authorID, "/?#") {
			continue
		}
		var author olAuthor
		if _, err := o.client.getJSON(ctx, olAuthorsRoute+authorID+".json", nil, &author); err != nil {
			continue
		}
		if name := cleanTitle(author.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// normaliseOLWork turns the three documents into one source: the work's identity, the edition facts and the
// authors, with the snapshot kept as the provider said it.
func normaliseOLWork(id string, work olWork, authors []string, editions olEditions, raw json.RawMessage) domain.Source {
	return domain.Source{
		Provider:    ProviderOpenLibrary,
		ProviderID:  id,
		Trust:       domain.TrustVerified,
		Title:       cleanTitle(work.Title),
		Creators:    olCreators(authors),
		Details:     olDetails(work, editions),
		Year:        olYear(work, editions),
		CoverSource: ProviderOpenLibrary,
		Raw:         raw,
	}
}

// olCreators turns author names into the merge's creators.
func olCreators(names []string) []domain.Creator {
	out := make([]domain.Creator, 0, len(names))
	for _, name := range names {
		out = append(out, domain.Creator{Role: "author", Name: name})
	}
	return out
}

// olYear is the work's first publication year, falling back to the earliest edition. The work's own year wins
// because the edition she read may be a reprint (PLAN.md 7).
func olYear(work olWork, editions olEditions) int {
	if year := yearFromDate(work.FirstPublishDate); year > 0 {
		return year
	}
	earliest := 0
	for _, edition := range editions.Entries {
		year := yearFromDate(edition.PublishDate)
		if year > 0 && (earliest == 0 || year < earliest) {
			earliest = year
		}
	}
	return earliest
}

// olDetails keeps the edition facts and the facts a card shows, as plain strings.
func olDetails(work olWork, editions olEditions) map[string]string {
	details := map[string]string{}
	put := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			details[key] = cleanTitle(value)
		}
	}
	put("first_published", work.FirstPublishDate)
	if len(work.Subjects) > 0 {
		put("subjects", strings.Join(firstN(work.Subjects, 8), ", "))
	}
	// The first edition listed wins the edition facts; the earliest year wins the year. A reprint further down
	// the list must not replace the edition she most likely read.
	putOnce := func(key, value string) {
		if details[key] == "" {
			put(key, value)
		}
	}
	earliest := 0
	for _, edition := range editions.Entries {
		putOnce("edition", edition.Title)
		if len(edition.Publishers) > 0 {
			putOnce("publisher", edition.Publishers[0])
		}
		if edition.Pages > 0 {
			putOnce("pages", strconv.Itoa(edition.Pages))
		}
		if len(edition.ISBN13) > 0 {
			putOnce("isbn_13", edition.ISBN13[0])
		} else if len(edition.ISBN10) > 0 {
			putOnce("isbn_10", edition.ISBN10[0])
		}
		if len(edition.Series) > 0 {
			putOnce("series", edition.Series[0])
		}
		if year := yearFromDate(edition.PublishDate); year > 0 && (earliest == 0 || year < earliest) {
			earliest = year
		}
	}
	if earliest > 0 {
		put("edition_year", strconv.Itoa(earliest))
	}
	return details
}

// olEnvelope stores what the source said as one document, so the audit trail keeps the work, the authors and the
// editions together without inventing a table for each.
func olEnvelope(work, editions []byte) json.RawMessage {
	envelope := map[string]json.RawMessage{"work": work}
	if len(editions) > 0 {
		envelope["editions"] = editions
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil
	}
	return encoded
}

// coverURL builds a cover URL for a cover id, or "" when the work has no cover.
func (o *OpenLibrary) coverURL(coverID int, size string) string {
	if coverID <= 0 {
		return ""
	}
	return fmt.Sprintf("%s/b/id/%d-%s.jpg", strings.TrimRight(o.coverBase, "/"), coverID, size)
}

// firstN returns at most n items of a list.
func firstN(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	return list[:n]
}
