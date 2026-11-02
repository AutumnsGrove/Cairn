package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// The model is the sender of every tool argument, so the decoder is a boundary with an untrusted sender. It must
// never panic and must never accept a second value or an unknown field (docs/TESTING.md 4).
func FuzzDecodeArgs(f *testing.F) {
	for _, seed := range []string{
		`{"title":"Dune"}`,
		`{"title":"Dune","opinion":"loved it"}`,
		`{"title":"Dune"}{"title":"Messiah"}`,
		`{"title":`,
		`["Dune"]`,
		`null`,
		``,
		`{"title":"` + strings.Repeat("a", 10000) + `"}`,
		`{"hints":{"feel":["moving"]}}`,
		`{"body_quotes":["a","b"],"verdict_quote":"a"}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		var in lookupArgs
		// The result is not interesting; the absence of a panic is.
		_ = decodeArgs(json.RawMessage(raw), &in)
	})
}

// The corpus is committed data, but it is still parsed at startup and a bad fixture should fail with an id, never
// with a panic.
func FuzzParseCorpus(f *testing.F) {
	f.Add([]byte(`[{"id":"a","group":"book","message":"finished it","expected":{}}]`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`[{"id":1}]`))
	f.Add([]byte(`[`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = ParseCorpus(raw)
	})
}
