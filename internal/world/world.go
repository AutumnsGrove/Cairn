// Package world is the in-process fake world: every outside service Cairn talks to, with fault injection, so
// tests and `cairn-dev` can drive the real service offline.
//
// It is imported only from `_test.go` files and `cmd/cairn-dev`; the architecture test fails the build if
// shipped code reaches for it (docs/BUILD.md 4). S00 builds the pattern once, with one trivial service; S05,
// S06, S15 and S16 add the fakes for the models, the providers, Polaris and R2.
package world

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// Clock is the injected time source. Nothing in Cairn calls time.Now directly, so a test can decide what
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
	Header http.Header
	At     time.Time
}

// Service is the trivial fake every later fake follows: a name, a fault per path, a recorder, and a handler
// a test can mount on an httptest server.
type Service struct {
	name   string
	clock  Clock
	mu     sync.Mutex
	faults map[string]Fault
	calls  []Request
}

// NewService makes a fake with a fixed clock, so recorded times are deterministic.
func NewService(name string, clock Clock) *Service {
	if clock == nil {
		clock = RealClock{}
	}
	return &Service{name: name, clock: clock, faults: make(map[string]Fault)}
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

// Requests returns a copy of the recorded calls, oldest first.
func (s *Service) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.calls))
	copy(out, s.calls)
	return out
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
	case FaultNone:
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
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

// Server is a Service mounted on a real loopback listener, for a test or for cairn-dev.
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
