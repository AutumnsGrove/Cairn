package world

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The world is the peer every later test drives Sepia against, so the harness itself is tested on the three
// routes before anything depends on it.

func get(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	return client.Do(req)
}

// --- happy ---------------------------------------------------------------------------------------------

func TestServiceAnswers(t *testing.T) {
	srv := Start("tmdb", NewFixedClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)))
	defer srv.Close()

	resp, err := get(t, srv.URL()+"/movie/603")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	if got := srv.Calls("/movie/603"); got != 1 {
		t.Errorf("recorded %d calls, want 1", got)
	}
	rec := srv.Requests()[0]
	if rec.Method != http.MethodGet || rec.Path != "/movie/603" {
		t.Errorf("recorded %+v", rec)
	}
	if want := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC); !rec.At.Equal(want) {
		t.Errorf("recorded at %v, want the fixed clock's %v", rec.At, want)
	}
}

func TestFaultCanBeCleared(t *testing.T) {
	srv := Start("polaris", nil)
	defer srv.Close()

	srv.SetFault("/ask", FaultError)
	resp, err := get(t, srv.URL()+"/ask")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != statusError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, statusError)
	}

	srv.ClearFault("/ask")
	resp, err = get(t, srv.URL()+"/ask")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status after clearing = %d, want 200", resp.StatusCode)
	}
}

// --- sad -----------------------------------------------------------------------------------------------

// The outage is sticky: the second and third call fail too, which is the whole point of the harness.
func TestErrorFaultIsSticky(t *testing.T) {
	srv := Start("openlibrary", nil)
	defer srv.Close()
	srv.SetFault("/works/OL1W", FaultError)

	for i := range 3 {
		resp, err := get(t, srv.URL()+"/works/OL1W")
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != statusError {
			t.Fatalf("call %d: status = %d, want %d", i+1, resp.StatusCode, statusError)
		}
	}
	if got := srv.Calls("/works/OL1W"); got != 3 {
		t.Errorf("recorded %d calls, want 3", got)
	}
}

func TestSlowFaultDelays(t *testing.T) {
	srv := Start("decisions", nil)
	defer srv.Close()
	srv.SetFault("/systemone", FaultSlow)

	start := time.Now()
	resp, err := get(t, srv.URL()+"/systemone")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if elapsed := time.Since(start); elapsed < slowDelay/2 {
		t.Errorf("answered in %v, want it to be slow", elapsed)
	}
}

// A slow reply must give up when the caller's context is cancelled, or a test could hang instead of failing.
func TestSlowFaultHonoursCancellation(t *testing.T) {
	srv := Start("decisions", NewFixedClock(time.Now()))
	defer srv.Close()
	srv.SetFault("/systemone", FaultSlow)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL()+"/systemone", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a cancelled request was answered")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "context") {
		t.Logf("client error (acceptable): %v", err)
	}
}

func TestDropFaultFailsTheCall(t *testing.T) {
	srv := Start("r2", nil)
	defer srv.Close()
	srv.SetFault("/bucket", FaultDrop)

	if resp, err := get(t, srv.URL()+"/bucket"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a dropped connection produced a response")
	}
}

// --- bad -----------------------------------------------------------------------------------------------

func TestHugeFaultExceedsEveryLimit(t *testing.T) {
	srv := Start("tmdb", nil)
	defer srv.Close()
	srv.SetFault("/movie/1", FaultHuge)

	resp, err := get(t, srv.URL()+"/movie/1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.ContentLength >= 0 && resp.ContentLength <= 2<<20 {
		t.Errorf("Content-Length = %d, want more than the 2 MiB provider limit", resp.ContentLength)
	}
}

func TestMalformedFaultIsNotJSON(t *testing.T) {
	srv := Start("tmdb", nil)
	defer srv.Close()
	srv.SetFault("/movie/2", FaultMalformed)

	resp, err := get(t, srv.URL()+"/movie/2")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if strings.HasPrefix(string(buf[:n]), `{"service"`) {
		t.Errorf("malformed fault returned a valid reply: %s", buf[:n])
	}
}

func TestRequestsReturnsACopy(t *testing.T) {
	srv := Start("tmdb", nil)
	defer srv.Close()
	resp, err := get(t, srv.URL()+"/a")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	got := srv.Requests()
	got[0].Path = "/mutated"
	if srv.Requests()[0].Path != "/a" {
		t.Error("Requests exposed the recorder's own slice")
	}
}

func TestIDs(t *testing.T) {
	ids := NewSeqIDs("draft")
	first, second := ids.New(), ids.New()
	if first == second {
		t.Fatalf("ids repeat: %q", first)
	}
	if !strings.HasPrefix(first, "draft-") {
		t.Errorf("id = %q, want a readable prefix", first)
	}
}

func TestFixedClock(t *testing.T) {
	c := NewFixedClock(time.Unix(0, 0))
	c.Advance(time.Hour)
	if got := c.Now().Unix(); got != 3600 {
		t.Fatalf("Now = %d, want 3600", got)
	}
}
