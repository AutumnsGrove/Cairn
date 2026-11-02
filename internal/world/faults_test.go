package world

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// These are the faults the provider clients need beyond the skeleton's five: a rate limit with and without
// Retry-After, a plain 404, a body that never ends, and a real fixture where the echo would do.

func TestServiceServesAFixture(t *testing.T) {
	srv := Start("tmdb", nil)
	defer srv.Close()
	srv.SetBody("/search/movie", []byte(`{"page":1,"results":[{"id":1,"title":"Invented"}]}`))

	resp, err := get(t, srv.URL()+"/search/movie?query=x")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Results []struct {
			Title string `json:"title"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("fixture body %q: %v", body, err)
	}
	if len(decoded.Results) != 1 || decoded.Results[0].Title != "Invented" {
		t.Errorf("body = %s", body)
	}
	// The path is still recorded, and the query string with it, because a provider test reads both.
	rec := srv.Requests()[0]
	if rec.Path != "/search/movie" {
		t.Errorf("recorded path = %q", rec.Path)
	}
}

func TestServiceRateLimit(t *testing.T) {
	for name, wantHeader := range map[string]bool{"without retry-after": false, "with retry-after": true} {
		t.Run(name, func(t *testing.T) {
			srv := Start("openlibrary", nil)
			defer srv.Close()
			if wantHeader {
				srv.SetRetryAfter(2 * time.Second)
			}
			srv.SetFault("/search.json", FaultRateLimit)

			for i := 0; i < 2; i++ {
				resp, err := get(t, srv.URL()+"/search.json?title=x")
				if err != nil {
					t.Fatalf("call %d: %v", i+1, err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusTooManyRequests {
					t.Fatalf("call %d status = %d, want 429", i+1, resp.StatusCode)
				}
				got := resp.Header.Get("Retry-After")
				if wantHeader && got != "2" {
					t.Errorf("call %d Retry-After = %q, want 2", i+1, got)
				}
				if !wantHeader && got != "" {
					t.Errorf("call %d Retry-After = %q, want none", i+1, got)
				}
			}
		})
	}
}

func TestServiceNotFound(t *testing.T) {
	srv := Start("tmdb", nil)
	defer srv.Close()
	srv.SetFault("/movie/999", FaultNotFound)

	resp, err := get(t, srv.URL()+"/movie/999")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// A peer that sends headers and then stops is the one failure a timeout has to catch. The client's own deadline
// is the only way out, so the test sets one and asserts it fired.
func TestServiceStall(t *testing.T) {
	srv := Start("tmdb", nil)
	defer srv.Close()
	srv.SetFault("/movie/1", FaultStall)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL()+"/movie/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 50 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return // the stall surfaced on the request itself, which is the other valid shape
	}
	// More often the status line and a few bytes arrive and the body never finishes: the read is where a stalled
	// peer shows, and a caller that does not bound it hangs here forever.
	defer func() { _ = resp.Body.Close() }()
	if _, readErr := io.ReadAll(resp.Body); readErr == nil {
		t.Fatal("reading a stalled body returned no error")
	}
}

// A cleared fault puts a fixture back, so a test can prove recovery without a second fake.
func TestServiceFaultThenFixture(t *testing.T) {
	srv := Start("tmdb", nil)
	defer srv.Close()
	srv.SetBody("/search/tv", []byte(`{"results":[]}`))
	srv.SetFault("/search/tv", FaultError)

	if resp, err := get(t, srv.URL()+"/search/tv"); err != nil {
		t.Fatalf("request: %v", err)
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("faulted status = %d", resp.StatusCode)
		}
	}

	srv.ClearFault("/search/tv")
	resp, err := get(t, srv.URL()+"/search/tv")
	if err != nil {
		t.Fatalf("request after clearing: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status after clearing = %d", resp.StatusCode)
	}
}
