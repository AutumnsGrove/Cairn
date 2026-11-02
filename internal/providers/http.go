package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// maxBody bounds one provider reply, from docs/STANDARDS.md 4a. A body one byte over is refused rather than
// truncated: half a JSON document is not a result.
const maxBody = 2 << 20

// maxTitleChars bounds a title from a provider. A title is shown and passed to the agent, so it is bounded like
// every other piece of untrusted text.
const maxTitleChars = 300

// cacheEntry is one reused answer.
type cacheEntry struct {
	at  time.Time
	val any
}

// cache is the bounded, in-memory answer cache: 128 entries and a short TTL over the injected clock. It never
// touches the database and a restart clears it, so it can never outlive the snapshot rules (S06 decisions 2).
type cache struct {
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]cacheEntry
}

// maxCacheEntries bounds the cache so a long session cannot grow it without limit.
const maxCacheEntries = 128

// newCache builds a cache with a TTL and a clock.
func newCache(ttl time.Duration, clock Clock) *cache {
	if clock == nil {
		clock = RealClock{}
	}
	return &cache{ttl: ttl, now: clock.Now, entries: make(map[string]cacheEntry)}
}

// get returns a live cached answer.
func (c *cache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || c.now().Sub(entry.at) >= c.ttl {
		if ok {
			delete(c.entries, key)
		}
		return nil, false
	}
	return entry.val, true
}

// put stores an answer, evicting the entry that expires first when the cache is full.
func (c *cache) put(key string, val any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxCacheEntries {
		oldestKey, oldest := "", time.Time{}
		for k, entry := range c.entries {
			if oldest.IsZero() || entry.at.Before(oldest) {
				oldestKey, oldest = k, entry.at
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = cacheEntry{at: c.now(), val: val}
}

// limiter paces calls. It measures real time on purpose: a rate limit is about how fast the far side is asked,
// not about the app's clock, and a test disables it with a negative interval.
type limiter struct {
	interval time.Duration
	mu       sync.Mutex
	next     time.Time
}

// wait blocks until this client may make another call, or the caller's context ends.
func (l *limiter) wait(ctx context.Context) error {
	if l.interval <= 0 {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	at := l.next
	l.next = at.Add(l.interval)
	l.mu.Unlock()

	delay := time.Until(at)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// client is the shared HTTP plumbing: one base URL, one key, one client, one limiter.
type client struct {
	provider  string
	baseURL   string
	keyParam  string // where the key goes in the query, empty when the provider needs no key
	key       string
	http      HTTPDoer
	limiter   *limiter
	userAgent string
}

// getJSON reads one JSON document into into, after pacing, with every failure classified.
//
// The key is added to the query here and never appears in an error: Error carries the provider, the route and a
// reason, and the route is a constant.
func (c *client) getJSON(ctx context.Context, route string, params map[string]string, into any) ([]byte, error) {
	if err := c.limiter.wait(ctx); err != nil {
		return nil, c.failure(ctx, route, err)
	}

	url := strings.TrimRight(c.baseURL, "/") + route
	values := make([]string, 0, len(params)+1)
	for name, value := range params {
		if value == "" {
			continue
		}
		values = append(values, name+"="+urlQueryEscape(value))
	}
	if c.keyParam != "" && c.key != "" {
		values = append(values, c.keyParam+"="+urlQueryEscape(c.key))
	}
	if len(values) > 0 {
		url += "?" + strings.Join(values, "&")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &Error{Provider: c.provider, Op: route, Reason: ReasonUnavailable, Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.failure(ctx, route, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if statusErr := c.status(route, resp); statusErr != nil {
		return nil, statusErr
	}

	body, err := readLimited(resp.Body)
	if err != nil {
		return nil, c.failure(ctx, route, err)
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" && !strings.HasPrefix(contentType, "application/json") {
		return nil, &Error{
			Provider: c.provider, Op: route, Reason: ReasonMalformed, Status: resp.StatusCode,
			Err: errors.New("the reply was " + contentType + ", not JSON"),
		}
	}
	if err := json.Unmarshal(body, into); err != nil {
		return nil, &Error{Provider: c.provider, Op: route, Reason: ReasonMalformed, Status: resp.StatusCode, Err: err}
	}
	return body, nil
}

// status turns a non-200 reply into the typed failure it means. It never reads the body, which may be large.
func (c *client) status(route string, resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return &Error{Provider: c.provider, Op: route, Reason: ReasonNotFound, Status: resp.StatusCode}
	case resp.StatusCode == http.StatusTooManyRequests:
		// A 429 with no Retry-After is the harder case: the caller must back off anyway, so it is retryable at
		// a default delay rather than a zero one.
		return &Error{
			Provider: c.provider, Op: route, Reason: ReasonRateLimited, Status: resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")), Retryable: true,
		}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &Error{Provider: c.provider, Op: route, Reason: ReasonUnauthorized, Status: resp.StatusCode}
	case resp.StatusCode >= 500:
		return &Error{Provider: c.provider, Op: route, Reason: ReasonUnavailable, Status: resp.StatusCode, Retryable: true}
	default:
		return &Error{Provider: c.provider, Op: route, Reason: ReasonUnavailable, Status: resp.StatusCode}
	}
}

// failure classifies a transport, deadline or read failure. The caller's own cancellation is returned untouched,
// because a cancelled turn is not an unavailable provider.
func (c *client) failure(ctx context.Context, route string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.Canceled) {
			return ctxErr
		}
		return &Error{Provider: c.provider, Op: route, Reason: ReasonTimeout, Retryable: true, Err: ctxErr}
	}
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return &Error{Provider: c.provider, Op: route, Reason: ReasonTimeout, Retryable: true, Err: err}
	case errors.Is(err, errTooLarge):
		return &Error{Provider: c.provider, Op: route, Reason: ReasonTooLarge, Err: err}
	default:
		return &Error{Provider: c.provider, Op: route, Reason: ReasonUnavailable, Retryable: true, Err: err}
	}
}

// errTooLarge is the read's own sentinel, so failure can tell a size refusal from a broken connection.
var errTooLarge = errors.New("providers: reply is larger than the provider limit")

// readLimited reads at most maxBody and refuses one byte more.
func readLimited(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, errTooLarge
	}
	return body, nil
}

// parseRetryAfter reads both forms the header allows: delay-seconds and an HTTP date. Anything else is zero, and
// the caller backs off by its own default rather than waiting forever.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if delay := time.Until(at); delay > 0 {
			return delay
		}
		return 0
	}
	return 0
}

// urlQueryEscape escapes a query value with spaces as %20, which TMDB and Open Library both accept.
func urlQueryEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// cleanText makes provider text safe to show and to bound: control and format characters go, whitespace is
// collapsed, and the result is cut at a rune boundary. A control character in a title is a provider bug or a
// hostile fixture; either way it never reaches a card or a model turn.
func cleanTitle(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
		case isUnsafeRune(r):
			// dropped
		default:
			b.WriteRune(r)
			lastSpace = false
		}
		if b.Len() >= maxTitleChars {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// isUnsafeRune reports a control or a bidirectional format character, which a title must never carry: a bidi
// override can make "drama" read as something else on the card, and a zero-width character can hide a word from a
// reader while a model still sees it.
func isUnsafeRune(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0x200b && r <= 0x200f: // zero-width space, joiners, left/right marks
		return true
	case r >= 0x202a && r <= 0x202e: // bidi embeddings and overrides
		return true
	case r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069: // word joiner, invisible operators, isolates
		return true
	case r == 0x061c, r == 0xfeff: // Arabic letter mark, byte-order mark
		return true
	}
	return false
}

// yearFromDate reads the year out of the YYYY-MM-DD shapes both providers send, tolerating a short or empty one.
func yearFromDate(date string) int {
	date = strings.TrimSpace(date)
	if len(date) < 4 {
		return 0
	}
	year, err := strconv.Atoi(date[:4])
	if err != nil || year < 1000 || year > 3000 {
		return 0
	}
	return year
}
