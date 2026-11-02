package providers

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The pacing is the one part of the clients that can hang a turn, so it is tested directly rather than only
// through a provider.
func TestLimiterWaitsAndCancels(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		l := &limiter{interval: -1}
		start := time.Now()
		if err := l.wait(context.Background()); err != nil {
			t.Fatalf("wait: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Millisecond {
			t.Errorf("disabled limiter waited %v", elapsed)
		}
	})

	t.Run("paces the second call", func(t *testing.T) {
		l := &limiter{interval: 30 * time.Millisecond}
		if err := l.wait(context.Background()); err != nil {
			t.Fatalf("first wait: %v", err)
		}
		start := time.Now()
		if err := l.wait(context.Background()); err != nil {
			t.Fatalf("second wait: %v", err)
		}
		if elapsed := time.Since(start); elapsed < 10*time.Millisecond {
			t.Errorf("second call waited %v, want it paced", elapsed)
		}
	})

	t.Run("a cancelled caller does not wait", func(t *testing.T) {
		l := &limiter{interval: 5 * time.Second}
		if err := l.wait(context.Background()); err != nil {
			t.Fatalf("first wait: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := l.wait(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait = %v, want the deadline", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("cancelled wait blocked for %v", elapsed)
		}
	})
}

// Retry-After arrives as seconds or as a date, and anything else must not become a wait of minutes.
func TestParseRetryAfter(t *testing.T) {
	cases := map[string]time.Duration{
		"":     0,
		"2":    2 * time.Second,
		"0":    0,
		"-5":   0,
		"soon": 0,
		"1.5":  0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
	future := time.Now().Add(90 * time.Second).UTC().Format(httpTimeFormat)
	if got := parseRetryAfter(future); got < 80*time.Second || got > 90*time.Second {
		t.Errorf("parseRetryAfter(date) = %v, want about 90s", got)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(httpTimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Errorf("parseRetryAfter(past date) = %v, want 0", got)
	}
}

// httpTimeFormat is RFC 1123 GMT, the only date form the header allows.
const httpTimeFormat = "Mon, 02 Jan 2006 15:04:05 GMT"

// cleanTitle is the whole defence against a hostile provider string, so each class it removes is named here.
func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"  plain  ":                    "plain",
		"one\n\ntwo":                   "one two",
		"nul\x00byte":                  "nulbyte",
		"bidi\u202eoverride":           "bidioverride",
		"zero\u200bwidth":              "zerowidth",
		"line\u2028separator":          "line separator",
		"keep \u00e9 and \u4e16\u754c": "keep \u00e9 and \u4e16\u754c",
	}
	for in, want := range cases {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
	long := make([]byte, 0, maxTitleChars+50)
	for i := 0; i < maxTitleChars+50; i++ {
		long = append(long, 'a')
	}
	if got := len(cleanTitle(string(long))); got > maxTitleChars {
		t.Errorf("cleanTitle kept %d characters, want at most %d", got, maxTitleChars)
	}
}
