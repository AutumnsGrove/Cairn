package domain

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Merging is how "one nice entry" is kept true without losing what a source said. The rules are in PLAN.md 7:
// the primary provider's title wins, the earliest year wins, creators and details are unioned, and a disagreement
// is recorded rather than quietly overwritten. A merge never deletes a source: work_sources is the audit trail.

// Trust says how much a source's fields may be believed.
type Trust string

const (
	// TrustVerified is an API that is canonical for the medium.
	TrustVerified Trust = "verified"
	// TrustUnverified is the web: it proposes, and nothing it says becomes canonical until it is accepted.
	TrustUnverified Trust = "unverified"
	// TrustAccepted is a web field the operator tapped to accept.
	TrustAccepted Trust = "accepted"
)

// Creator is a person or studio and the role they had. Role is data, not a column, so a new medium needs no
// migration.
type Creator struct {
	Role string `json:"role"`
	Name string `json:"name"`
}

// Citation is where a web-derived field came from.
type Citation struct {
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Source is one provider's answer about a work, exactly as it arrived. It is never modified by a merge.
type Source struct {
	Provider        string            `json:"provider"`
	ProviderID      string            `json:"provider_id"`
	Trust           Trust             `json:"trust"`
	Title           string            `json:"title"`
	OriginalTitle   string            `json:"original_title,omitempty"`
	Year            int               `json:"year,omitempty"`
	Creators        []Creator         `json:"creators,omitempty"`
	Details         map[string]string `json:"details,omitempty"`
	AlternateTitles []string          `json:"alternate_titles,omitempty"`
	CoverPath       string            `json:"cover_path,omitempty"`
	CoverSource     string            `json:"cover_source,omitempty"`
	Citations       []Citation        `json:"citations,omitempty"`
	PolarisThreadID string            `json:"polaris_thread_id,omitempty"`
	Raw             json.RawMessage   `json:"raw,omitempty"`
}

// Conflict is a disagreement between sources that the merge kept a winner for, so the "why" sheet can explain it.
type Conflict struct {
	Field    string `json:"field"`
	Kept     string `json:"kept"`
	Rejected string `json:"rejected"`
	From     string `json:"from"` // the provider whose value was not kept
}

// Work is the canonical thing an entry is about, after a merge.
type Work struct {
	Medium          string            `json:"medium"`
	Title           string            `json:"title"`
	OriginalTitle   string            `json:"original_title,omitempty"`
	Year            int               `json:"year,omitempty"`
	Creators        []Creator         `json:"creators,omitempty"`
	Details         map[string]string `json:"details,omitempty"`
	AlternateTitles []string          `json:"alternate_titles,omitempty"`
	CoverPath       string            `json:"cover_path,omitempty"`
	CoverSource     string            `json:"cover_source,omitempty"`
	PrimaryProvider string            `json:"primary_provider,omitempty"`
	PrimaryID       string            `json:"primary_id,omitempty"`
	// Verified is false for a manual work logged during an outage; a later "fill in details" can verify it.
	Verified bool `json:"verified"`
	// Sources is every source that contributed, in the order they arrived. A merge never removes one.
	Sources []Source `json:"sources,omitempty"`
}

// MergeResult is the merged work plus the disagreements it resolved.
type MergeResult struct {
	Work      Work
	Conflicts []Conflict
}

// trusted reports whether a source's fields may become canonical. The web never does until the operator accepts
// a field; a manual work is the operator's own typing, which is the most trusted input there is.
func trusted(s Source) bool {
	switch {
	case s.Provider == "manual":
		return true
	case s.Trust == TrustVerified, s.Trust == TrustAccepted:
		return true
	default:
		return false
	}
}

// Merge folds sources into one work. It is idempotent: merging the result's own sources again produces the same
// work, because a source is stored as it arrived and the rules only read.
func Merge(medium string, sources []Source) MergeResult {
	result := MergeResult{Work: Work{Medium: medium, Sources: cloneSources(sources)}}
	if len(sources) == 0 {
		return result
	}

	primary := -1
	for i, s := range sources {
		if trusted(s) && strings.TrimSpace(s.Title) != "" {
			primary = i
			break
		}
	}
	if primary < 0 {
		// Nothing trusted: the work is unverified, and the fields stay empty so the card can offer to fill them.
		for i, s := range sources {
			if strings.TrimSpace(s.Title) != "" {
				primary = i
				break
			}
		}
		if primary < 0 {
			return result
		}
	}

	w := &result.Work
	w.Title = strings.TrimSpace(sources[primary].Title)
	w.OriginalTitle = strings.TrimSpace(sources[primary].OriginalTitle)
	w.PrimaryProvider = sources[primary].Provider
	w.PrimaryID = sources[primary].ProviderID
	w.CoverPath = sources[primary].CoverPath
	w.CoverSource = sources[primary].CoverSource
	w.Details = map[string]string{}

	// Years: the earliest original release wins, and every disagreement is recorded.
	yearFrom := ""
	for _, s := range sources {
		if !trusted(s) || s.Year <= 0 {
			continue
		}
		switch {
		case w.Year == 0:
			w.Year = s.Year
			yearFrom = s.Provider
		case s.Year < w.Year:
			result.Conflicts = append(result.Conflicts, Conflict{
				Field: "year", Kept: strconv.Itoa(s.Year), Rejected: strconv.Itoa(w.Year), From: yearFrom,
			})
			w.Year = s.Year
			yearFrom = s.Provider
		case s.Year > w.Year:
			result.Conflicts = append(result.Conflicts, Conflict{
				Field: "year", Kept: strconv.Itoa(w.Year), Rejected: strconv.Itoa(s.Year), From: s.Provider,
			})
		}
	}

	// Creators: the union, deduplicated by normalised name and role, primary's first.
	seen := map[string]bool{}
	addCreator := func(c Creator) {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return
		}
		key := normaliseName(name) + "\x00" + strings.ToLower(strings.TrimSpace(c.Role))
		if seen[key] {
			return
		}
		seen[key] = true
		w.Creators = append(w.Creators, Creator{Role: strings.TrimSpace(c.Role), Name: name})
	}
	for _, s := range orderedByPrimary(sources, primary) {
		if !trusted(s) {
			continue
		}
		for _, c := range s.Creators {
			addCreator(c)
		}
	}

	// Details: union, with a conflict recorded when two sources disagree about the same key.
	detailFrom := map[string]string{}
	for _, s := range orderedByPrimary(sources, primary) {
		if !trusted(s) {
			continue
		}
		keys := make([]string, 0, len(s.Details))
		for k := range s.Details {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := strings.TrimSpace(s.Details[k])
			if v == "" {
				continue
			}
			kept, ok := w.Details[k]
			switch {
			case !ok:
				w.Details[k] = v
				detailFrom[k] = s.Provider
			case kept != v:
				result.Conflicts = append(result.Conflicts, Conflict{
					Field: k, Kept: kept, Rejected: v, From: s.Provider,
				})
			}
		}
	}

	// The cover comes from the primary; a later source only fills an empty one.
	if w.CoverPath == "" {
		for _, s := range orderedByPrimary(sources, primary) {
			if trusted(s) && s.CoverPath != "" {
				w.CoverPath = s.CoverPath
				w.CoverSource = s.CoverSource
				break
			}
		}
	}

	// Alternate titles: every other title any trusted source used, deduplicated and never the canonical one.
	titleKey := NormaliseTitle(w.Title)
	altSeen := map[string]bool{titleKey: true}
	addAlt := func(title string) {
		title = strings.TrimSpace(title)
		if title == "" {
			return
		}
		key := NormaliseTitle(title)
		if altSeen[key] {
			return
		}
		altSeen[key] = true
		w.AlternateTitles = append(w.AlternateTitles, title)
	}
	for _, s := range orderedByPrimary(sources, primary) {
		if !trusted(s) {
			continue
		}
		addAlt(s.Title)
		addAlt(s.OriginalTitle)
		for _, alt := range s.AlternateTitles {
			addAlt(alt)
		}
	}

	for _, s := range sources {
		if trusted(s) && s.Trust == TrustVerified {
			w.Verified = true
		}
	}

	sortConflicts(result.Conflicts)
	return result
}

// orderedByPrimary returns the sources with the primary first, so ties resolve the same way every time.
func orderedByPrimary(sources []Source, primary int) []Source {
	out := make([]Source, 0, len(sources))
	out = append(out, sources[primary])
	for i, s := range sources {
		if i != primary {
			out = append(out, s)
		}
	}
	return out
}

// sortConflicts makes the conflict list deterministic, so a merge of the same sources compares equal.
func sortConflicts(conflicts []Conflict) {
	sort.SliceStable(conflicts, func(i, j int) bool {
		if conflicts[i].Field != conflicts[j].Field {
			return conflicts[i].Field < conflicts[j].Field
		}
		if conflicts[i].From != conflicts[j].From {
			return conflicts[i].From < conflicts[j].From
		}
		return conflicts[i].Rejected < conflicts[j].Rejected
	})
}

// cloneSources copies the slice so a caller cannot mutate a merge result through the input.
func cloneSources(sources []Source) []Source {
	if len(sources) == 0 {
		return nil
	}
	out := make([]Source, len(sources))
	copy(out, sources)
	return out
}
