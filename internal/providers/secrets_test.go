package providers

import (
	"errors"
	"strings"
	"testing"
)

// A provider error is a log line. An http client's own error can carry the whole URL, and the URL carries the TMDB
// key, so the error type is the last place that can redact it (docs/BUILD.md invariant 9).
func TestErrorRedactsAKeyInAURL(t *testing.T) {
	cases := map[string]string{
		"tmdb query": `Get "https://provider.invalid/3/search/movie?include_adult=false&query=Dune&api_key=SECRET123": context deadline exceeded`,
		"key first":  `Get "https://example.test/path?api_key=SECRET123&x=1": boom`,
		"case":       `API_KEY=SECRET123`,
		"no key":     `Get "https://provider.invalid/search.json?q=Dune": boom`,
	}
	for name, cause := range cases {
		t.Run(name, func(t *testing.T) {
			err := &Error{Provider: "tmdb", Op: "search", Reason: ReasonTimeout, Err: errors.New(cause)}
			got := err.Error()
			if strings.Contains(got, "SECRET123") {
				t.Fatalf("the error leaked the key: %s", got)
			}
			if strings.Contains(cause, "SECRET123") && !strings.Contains(got, "REDACTED") {
				t.Fatalf("the error did not mark the redaction: %s", got)
			}
		})
	}
}
