package world

// The sample world `sepia-dev` runs against. The fixtures are invented and deliberately dull so that a frontend test
// can type a fixed sentence and get a card, without a key, a network or a real review (docs/BUILD.md invariant 10).
//
// The chat script quotes one sentence of that message, so the same words must appear in what the frontend types:
//
//	finished the lantern keeper last night, 4.5, the ending landed but it was too long
//
// The dev stack prints that sentence at startup; the end-to-end suite types it.

// SampleMessage is the sentence the dev stack's script is written for.
const SampleMessage = "finished the lantern keeper last night, 4.5, the ending landed but it was too long"

// SampleTMDB is a small, invented film and TV library.
func SampleTMDB() map[string][]byte {
	return map[string][]byte{
		"/search/movie": []byte(`{"page": 1, "results": [
	  {"id": 101, "title": "The Lantern Keeper", "original_title": "The Lantern Keeper",
	   "release_date": "2019-03-08", "poster_path": "/lantern.jpg"}
	], "total_results": 1}`),
		"/movie/101": []byte(`{"id": 101, "imdb_id": "tt0000101",
	  "title": "The Lantern Keeper", "original_title": "The Lantern Keeper",
	  "release_date": "2019-03-08", "runtime": 118, "original_language": "en",
	  "poster_path": "/lantern.jpg", "genres": [{"id": 18, "name": "Drama"}],
	  "credits": {"cast": [{"name": "Ilse Marrow"}], "crew": [{"job": "Director", "name": "Ada Voss"}]},
	  "external_ids": {"imdb_id": "tt0000101"}}`),
		"/search/tv": []byte(`{"page": 1, "results": [
	  {"id": 202, "name": "The Quarry Line", "original_name": "The Quarry Line",
	   "first_air_date": "2016-06-02", "poster_path": "/quarry.jpg"}
	], "total_results": 1}`),
		"/tv/202": []byte(`{"id": 202, "name": "The Quarry Line", "original_name": "The Quarry Line",
	  "first_air_date": "2016-06-02", "number_of_seasons": 3, "number_of_episodes": 24,
	  "status": "Ended", "original_language": "en", "poster_path": "/quarry.jpg",
	  "genres": [{"id": 9648, "name": "Mystery"}],
	  "created_by": [{"name": "Nell Farrier"}],
	  "credits": {"cast": [{"name": "Ruth Okafor"}], "crew": []},
	  "external_ids": {"imdb_id": "tt0000202", "tvdb_id": 500202}}`),
		"/configuration": []byte(`{"images": {"secure_base_url": "https://img.example/t/p/",
	  "poster_sizes": ["w92", "w342", "original"]}}`),
	}
}

// SampleOpenLibrary is the same invented library on Open Library's shape.
func SampleOpenLibrary() map[string][]byte {
	return map[string][]byte{
		"/search.json": []byte(`{"numFound": 2, "docs": [
	  {"key": "/works/OL1W", "title": "The Lantern Keeper", "author_name": ["Ada Voss"],
	   "first_publish_year": 1999, "cover_i": 123, "edition_key": ["OL1M"]},
	  {"key": "/works/OL2W", "title": "Lantern Keepers at Sea", "author_name": ["Tomas Reed"],
	   "first_publish_year": 2004}
	]}`),
		"/works/OL1W.json": []byte(`{"key": "/works/OL1W", "title": "The Lantern Keeper",
	  "first_publish_date": "1999", "covers": [123], "subjects": ["Lighthouses", "Memory"],
	  "authors": [{"author": {"key": "/authors/OL9A"}}]}`),
		"/works/OL1W/editions.json": []byte(`{"entries": [
	  {"title": "The Lantern Keeper", "publish_date": "1999", "publishers": ["Northlight Press"],
	   "number_of_pages": 312, "isbn_13": ["9780000000001"], "series": ["Lantern"], "covers": [123]}
	]}`),
		"/authors/OL9A.json": []byte(`{"name": "Ada Voss"}`),
	}
}

// SampleChatScript is one whole review, scripted: look the work up, take the one match, quote one sentence of the
// sample message and finish. It never asks a question, so the dev stack finishes a turn without a tap and the
// frontend can watch a card appear.
func SampleChatScript() []ChatTurn {
	return []ChatTurn{
		{ToolCalls: []ChatToolCall{{
			ID: "c1", Name: "media_lookup",
			Arguments: `{"title":"The Lantern Keeper","medium":"book"}`,
		}}},
		{ToolCalls: []ChatToolCall{{
			ID: "c2", Name: "merge_candidates",
			Arguments: `{"candidate_id":"openlibrary|OL1W"}`,
		}}},
		{ToolCalls: []ChatToolCall{{
			ID: "c3", Name: "save_entry",
			Arguments: `{"body_quotes":["the ending landed but it was too long"],"verdict_quote":"the ending landed"}`,
		}}},
		{Text: "Ready."},
	}
}

// ScriptSampleDecisions gives the pre-read a confident answer for the sample message: a book, a print read, no
// revisit and nothing the quiet options would leave open.
func ScriptSampleDecisions(d *Decisions) {
	d.Set("intent", map[string]float64{"log_review": 0.95, "chitchat": 0.02})
	d.Set("medium", map[string]float64{"book": 0.90, "film": 0.03, "unknown": 0.02})
	d.Set("format", map[string]float64{"print": 0.10, "not_stated": 0.80})
	d.Set("revisit", map[string]float64{"no": 0.94, "yes": 0.03})
	d.Set("mentions_story", map[string]float64{"yes": 0.90, "no": 0.10})
	d.Set("mentions_pacing", map[string]float64{"yes": 0.88, "no": 0.12})
	d.Set("extract_fidelity", map[string]float64{"faithful": 0.94, "drops_opinion": 0.04})
}

// LoadBodies installs a fixture set on a running fake.
func (s *Server) LoadBodies(bodies map[string][]byte) {
	for path, body := range bodies {
		s.SetBody(path, body)
	}
}
