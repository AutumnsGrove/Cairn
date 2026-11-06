// Package world is the in-process fake world: every outside service Sepia talks to, with fault injection, so
// tests and `sepia-dev` can drive the real service offline.
//
// It is imported only from `_test.go` files and `cmd/sepia-dev`; the architecture test fails the build if
// shipped code reaches for it (docs/BUILD.md 4). S00 builds the pattern once, with one trivial service; S05
// adds the decisions backend (decisions.go, over nova's decide contract); S06, S15 and S16 add the metadata
// providers, Polaris and R2.
package world

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"time"
)

// Clock is the injected time source. Nothing in Sepia calls time.Now directly, so a test can decide what
// "last night" or "three days ago" means (docs/STANDARDS.md 2).
type Clock interface {
	Now() time.Time
}

// RealClock is the clock the service runs with.
type RealClock struct{}

// Now returns the current time.
func (RealClock) Now() time.Time { return time.Now() }

// FixedClock is a clock a test can move by hand.
type FixedClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFixedClock starts a clock at t.
func NewFixedClock(t time.Time) *FixedClock { return &FixedClock{now: t} }

// Now returns the clock's current instant.
func (c *FixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward, which is how a test makes time pass without sleeping.
func (c *FixedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// IDs is the injected identifier source. Ids are deterministic in tests and never derived from a clock.
type IDs interface {
	// New returns a new identifier. It is never empty and never repeats.
	New() string
}

// SeqIDs returns "prefix-1", "prefix-2" and so on. Safe for concurrent use.
type SeqIDs struct {
	prefix string
	mu     sync.Mutex
	n      int
}

// NewSeqIDs returns an identifier source with a readable prefix.
func NewSeqIDs(prefix string) *SeqIDs { return &SeqIDs{prefix: prefix} }

// New returns the next identifier.
func (s *SeqIDs) New() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return fmt.Sprintf("%s-%d", s.prefix, s.n)
}

// Fault is a way the world misbehaves. A fault is sticky: it lasts until it is cleared, because an outage
// that clears on the second try would hide every retry bug (docs/TESTING.md 1).
type Fault string

const (
	// FaultNone means the service behaves.
	FaultNone Fault = ""
	// FaultSlow delays the reply, and keeps delaying it on every call.
	FaultSlow Fault = "slow"
	// FaultError answers 503, and keeps answering it.
	FaultError Fault = "error"
	// FaultMalformed answers 200 with a body that is not the JSON the caller expects.
	FaultMalformed Fault = "malformed"
	// FaultHuge answers 200 with a body larger than any provider limit.
	FaultHuge Fault = "huge"
	// FaultDrop closes the connection with no reply at all.
	FaultDrop Fault = "drop"
	// FaultRateLimit answers 429, with Retry-After only when the fake was told to send one.
	FaultRateLimit Fault = "rate_limited"
	// FaultNotFound answers 404, the ordinary "I have never heard of that id".
	FaultNotFound Fault = "not_found"
	// FaultStall sends headers and half a body, then never finishes, so a deadline is the only way out.
	FaultStall Fault = "stall"
)

// Metrics on the fake: a huge reply is one byte over the provider limit in docs/STANDARDS.md 4a, and a slow
// reply is long enough to trip a short deadline.
const (
	hugeBody    = 2<<20 + 1
	slowDelay   = 250 * time.Millisecond
	statusError = http.StatusServiceUnavailable
)

// Request is one recorded call. Bodies are deliberately not kept: a recorder must never become a copy of the
// operator's review text.
type Request struct {
	Method string
	Path   string
	// Query is the raw query string. It is recorded because it is the only place a provider request can carry
	// anything at all, and a test asserts what a client sends (never review text).
	Query  string
	Header http.Header
	At     time.Time
}

// Service is the trivial fake every later fake follows: a name, a fault per path, a recorder, and a handler
// a test can mount on an httptest server.
type Service struct {
	name         string
	clock        Clock
	mu           sync.Mutex
	faults       map[string]Fault
	bodies       map[string][]byte
	contentTypes map[string]string
	calls        []Request
	// retryAfter is the value FaultRateLimit advertises. Zero sends no header, which is the harder case.
	retryAfter time.Duration
}

// NewService makes a fake with a fixed clock, so recorded times are deterministic.
func NewService(name string, clock Clock) *Service {
	if clock == nil {
		clock = RealClock{}
	}
	return &Service{
		name: name, clock: clock,
		faults: make(map[string]Fault), bodies: make(map[string][]byte), contentTypes: make(map[string]string),
	}
}

// Name is the fake's name, used in its replies and in a failure message.
func (s *Service) Name() string { return s.name }

// SetFault makes every reply on path misbehave until ClearFault is called.
func (s *Service) SetFault(path string, f Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults[path] = f
}

// ClearFault puts a path back to behaving.
func (s *Service) ClearFault(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.faults, path)
}

// Fault returns the fault set on a path.
func (s *Service) Fault(path string) Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.faults[path]
}

// SetBody gives a path a real reply instead of the echo. This is how a fixture becomes a provider's answer
// without a second fake: the faults, the recorder and the clock stay the same.
func (s *Service) SetBody(path string, body []byte) {
	s.SetBodyType(path, "application/json", body)
}

// SetBodyType gives a path a body and its content type, which is how a cover download gets an image instead of
// JSON.
func (s *Service) SetBodyType(path, contentType string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies[path] = append([]byte(nil), body...)
	s.contentTypes[path] = contentType
}

// SetRetryAfter sets the Retry-After a 429 advertises. Zero means a 429 with no header.
func (s *Service) SetRetryAfter(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryAfter = d
}

// body returns a path's fixture, if it has one.
func (s *Service) body(path string) ([]byte, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.bodies[path]
	if !ok {
		return nil, "", false
	}
	contentType := s.contentTypes[path]
	if contentType == "" {
		contentType = "application/json"
	}
	return body, contentType, true
}

// Requests returns a copy of the recorded calls, oldest first.
func (s *Service) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.calls))
	copy(out, s.calls)
	return out
}

// RetryAfter returns the Retry-After value the fake advertises with a 429.
func (s *Service) RetryAfter() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retryAfter
}

// Calls returns how many times a path was requested.
func (s *Service) Calls(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.calls {
		if r.Path == path {
			n++
		}
	}
	return n
}

// ServeHTTP answers one request, after making it misbehave if a fault is set.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fault := s.Fault(r.URL.Path)
	s.record(r)

	switch fault {
	case FaultSlow:
		select {
		case <-time.After(slowDelay):
		case <-r.Context().Done():
			return
		}
	case FaultError:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusError)
		_, _ = fmt.Fprintf(w, `{"error":"%s is unavailable"}`, s.name)
		return
	case FaultMalformed:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"this": is not json`)
		return
	case FaultHuge:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, hugeBody))
		return
	case FaultDrop:
		dropConnection(w)
		return
	case FaultRateLimit:
		w.Header().Set("Content-Type", "application/json")
		if s.RetryAfter() > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(s.RetryAfter().Seconds())))
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprintf(w, `{"status_code":429,"status_message":"%s asks you to slow down"}`, s.name)
		return
	case FaultNotFound:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"status_code":404,"status_message":"%s has never heard of that"}`, s.name)
		return
	case FaultStall:
		// Headers and a partial body, then nothing until the client gives up. Without a deadline a caller would
		// wait forever, which is exactly the bug this fault exists to catch.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"page":1,`))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
		return
	case FaultNone:
	}

	w.Header().Set("Cache-Control", "no-store")
	if body, contentType, ok := s.body(r.URL.Path); ok {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// encoding/json escapes the path, and the path has already been cleaned by net/http. A fake that echoes
	// what it was asked is the point of it.
	_ = json.NewEncoder(w).Encode(map[string]string{"service": s.name, "path": r.URL.Path}) //nolint:gosec // G705: escaped by encoding/json
}

func (s *Service) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, Request{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		// Only the headers a caller can rely on; never the body, which is where a review would be.
		Header: r.Header.Clone(),
		At:     s.clock.Now(),
	})
}

// dropConnection closes the TCP connection without a status line, which is what a dead peer looks like.
func dropConnection(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		// Not every writer can hijack; a panic-free close of the body is the closest honest stand-in.
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	_ = conn.Close()
}

// Server is a Service mounted on a real loopback listener, for a test or for sepia-dev.
type Server struct {
	*Service
	http *httptest.Server
}

// Start mounts the fake on a loopback listener.
func Start(name string, clock Clock) *Server {
	svc := NewService(name, clock)
	ts := httptest.NewServer(svc)
	return &Server{Service: svc, http: ts}
}

// URL is the base URL of the running fake.
func (s *Server) URL() string { return s.http.URL }

// Close stops the listener.
func (s *Server) Close() { s.http.Close() }
