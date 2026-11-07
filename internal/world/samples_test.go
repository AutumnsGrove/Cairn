package world

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// The dev stack types one fixed sentence and expects a card, and the end-to-end suite types the same words. The
// script, the fixtures and the sentence therefore have to agree: a quote the sample message does not contain would
// make `sepia-dev` show a card the save guard refuses, and a title the fixtures do not carry would make the lookup
// come back empty with no model and no network to blame.

func TestSampleChatScriptQuotesTheSampleMessage(t *testing.T) {
	quotes := 0
	for _, turn := range SampleChatScript() {
		for _, call := range turn.ToolCalls {
			var args struct {
				BodyQuotes   []string `json:"body_quotes"`
				VerdictQuote string   `json:"verdict_quote"`
				Title        string   `json:"title"`
			}
			if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
				t.Fatalf("%s arguments are not JSON: %v", call.Name, err)
			}
			for _, quote := range args.BodyQuotes {
				quotes++
				if !strings.Contains(SampleMessage, quote) {
					t.Errorf("the script quotes %q, which is not in the sample message", quote)
				}
			}
			if args.VerdictQuote != "" && !strings.Contains(SampleMessage, args.VerdictQuote) {
				t.Errorf("the verdict quote %q is not in the sample message", args.VerdictQuote)
			}
			if args.Title != "" && args.Title != "The Lantern Keeper" {
				t.Errorf("the lookup asks for %q, which the fixtures do not carry", args.Title)
			}
		}
	}
	if quotes == 0 {
		t.Fatal("the sample script never quotes her")
	}
}

// The two provider fixtures are searched by the title the script looks up, so the same invented work has to be in
// both, with a detail or two the card can show.
func TestSampleFixturesCarryTheLanternKeeper(t *testing.T) {
	tmdb := SampleTMDB()
	search, ok := tmdb["/search/movie"]
	if !ok {
		t.Fatal("the TMDB fixture has no search route")
	}
	if !strings.Contains(string(search), "The Lantern Keeper") {
		t.Error("the TMDB search fixture does not carry the sample work")
	}
	detail, ok := tmdb["/movie/101"]
	if !ok {
		t.Fatal("the TMDB fixture has no detail route for the search result it returns")
	}
	if !strings.Contains(string(detail), "Ada Voss") {
		t.Error("the TMDB detail fixture lost its director")
	}

	openLibrary := SampleOpenLibrary()
	if !strings.Contains(string(openLibrary["/search.json"]), "The Lantern Keeper") {
		t.Error("the Open Library search fixture does not carry the sample work")
	}
	var work struct {
		Subjects []string `json:"subjects"`
	}
	if err := json.Unmarshal(openLibrary["/works/OL1W.json"], &work); err != nil {
		t.Fatalf("the work fixture is not JSON: %v", err)
	}
	if len(work.Subjects) == 0 {
		t.Error("the work fixture has no subjects, so the card would show no genres")
	}
}

// LoadBodies is how a rig installs a whole fixture set at once, and the fake must then serve each path.
func TestLoadBodiesServesEveryFixture(t *testing.T) {
	srv := Start("tmdb", nil)
	defer srv.Close()
	srv.LoadBodies(SampleTMDB())
	for path, want := range SampleTMDB() {
		resp, err := get(t, srv.URL()+path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !strings.Contains(string(body), strings.TrimSpace(string(want))[:20]) {
			t.Errorf("%s did not serve its fixture: %s", path, body)
		}
	}
}
