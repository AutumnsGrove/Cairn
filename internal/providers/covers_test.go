package providers

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/cairn/internal/world"
)

// pngBytes builds a real image of the given size, so the cover checks run against the decoder they will run
// against in production rather than against a stub.
func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width && x < 4; x++ {
		img.Set(x, 0, color.RGBA{R: 200, G: 30, B: 30, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

type coverRig struct {
	srv    *world.Server
	finder *Finder
	cfg    Config
	clock  *world.FixedClock
}

func newCoverRig(t *testing.T, tweak func(*Config, *world.Service)) *coverRig {
	t.Helper()
	clock := world.NewFixedClock(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	srv := world.Start("covers", clock)
	t.Cleanup(srv.Close)
	srv.SetBodyType("/cover.png", "image/png", pngBytes(t, 200, 300))

	cfg := Config{
		TMDBKey:                 "test-key",
		TMDBBaseURL:             srv.URL(),
		TMDBCoverBase:           srv.URL(),
		OpenLibraryBaseURL:      srv.URL(),
		OpenLibraryCoverBase:    srv.URL(),
		TMDBCallInterval:        -1,
		OpenLibraryCallInterval: -1,
		DataDir:                 t.TempDir(),
		Clock:                   clock,
	}
	if tweak != nil {
		tweak(&cfg, srv.Service)
	}
	return &coverRig{srv: srv, finder: NewFinder(cfg), cfg: cfg, clock: clock}
}

func TestCoverDownloadsAndStores(t *testing.T) {
	rig := newCoverRig(t, nil)
	cover, err := rig.finder.Cover(context.Background(), ProviderTMDB, "movie:101", rig.srv.URL()+"/cover.png")
	if err != nil {
		t.Fatalf("Cover: %v", err)
	}
	if cover.Path != "covers/tmdb-movie-101.png" {
		t.Errorf("path = %q", cover.Path)
	}
	if cover.ContentType != "image/png" || cover.Bytes <= 0 {
		t.Errorf("cover = %+v", cover)
	}
	if !cover.FetchedAt.Equal(rig.clock.Now()) {
		t.Errorf("fetched at %v, want %v", cover.FetchedAt, rig.clock.Now())
	}
	full := filepath.Join(rig.cfg.DataDir, cover.Path)
	if _, statErr := os.Stat(full); statErr != nil {
		t.Fatalf("stored file: %v", statErr)
	}

	// A second call inside the freshness window uses the file and touches nothing.
	again, err := rig.finder.Cover(context.Background(), ProviderTMDB, "movie:101", rig.srv.URL()+"/cover.png")
	if err != nil {
		t.Fatalf("second Cover: %v", err)
	}
	if again.Path != cover.Path {
		t.Errorf("second path = %q", again.Path)
	}
	if got := rig.srv.Calls("/cover.png"); got != 1 {
		t.Errorf("%d downloads, want 1", got)
	}
}

// The six-month cap is enforced when a cover is read, so no background job has to remember it (G3).
func TestCoverRefreshesAfterSixMonths(t *testing.T) {
	rig := newCoverRig(t, nil)
	first, err := rig.finder.Cover(context.Background(), ProviderTMDB, "movie:101", rig.srv.URL()+"/cover.png")
	if err != nil {
		t.Fatalf("Cover: %v", err)
	}
	rig.clock.Advance(CoverMaxAge - time.Hour)
	if _, midErr := rig.finder.Cover(context.Background(), ProviderTMDB, "movie:101", rig.srv.URL()+"/cover.png"); midErr != nil {
		t.Fatalf("Cover before the cap: %v", midErr)
	}
	if got := rig.srv.Calls("/cover.png"); got != 1 {
		t.Fatalf("%d downloads before the cap, want 1", got)
	}

	rig.clock.Advance(2 * time.Hour)
	second, err := rig.finder.Cover(context.Background(), ProviderTMDB, "movie:101", rig.srv.URL()+"/cover.png")
	if err != nil {
		t.Fatalf("Cover after the cap: %v", err)
	}
	if got := rig.srv.Calls("/cover.png"); got != 2 {
		t.Errorf("%d downloads after the cap, want 2", got)
	}
	if second.FetchedAt.Equal(first.FetchedAt) {
		t.Error("the refreshed cover kept the old fetch time")
	}
}

func TestCoverRefusesWhatItCannotTrust(t *testing.T) {
	cases := map[string]struct {
		url      string
		tweak    func(*world.Service)
		want     string
		wantFile bool
	}{
		"no url":       {url: "", want: ReasonNotFound},
		"not found":    {url: "/missing.png", tweak: func(s *world.Service) { s.SetFault("/missing.png", world.FaultNotFound) }, want: ReasonNotFound},
		"html":         {url: "/page.html", tweak: func(s *world.Service) { s.SetBodyType("/page.html", "text/html", []byte("<html>")) }, want: ReasonNotAnImage},
		"broken image": {url: "/broken.png", tweak: func(s *world.Service) { s.SetBodyType("/broken.png", "image/png", []byte("not a png")) }, want: ReasonNotAnImage},
		"dimensions":   {url: "/big.png", tweak: func(s *world.Service) { s.SetBodyType("/big.png", "image/png", pngBytes(t, 4001, 2)) }, want: ReasonCoverTooBig},
		"huge": {url: "/huge.png", tweak: func(s *world.Service) {
			// An image far over the size cap, with the content type a real cover would have, so the size check
			// fires before anything is decoded.
			body := append(pngBytes(t, 2, 2), make([]byte, MaxCoverBytes)...)
			s.SetBodyType("/huge.png", "image/png", body)
		}, want: ReasonCoverTooBig},
		"other host":   {url: "https://evil.example/cover.png", want: ReasonUnauthorized},
		"server error": {url: "/error.png", tweak: func(s *world.Service) { s.SetFault("/error.png", world.FaultError) }, want: ReasonUnavailable},
		"too slow":     {url: "/slow.png", tweak: func(s *world.Service) { s.SetFault("/slow.png", world.FaultSlow) }, want: ReasonTimeout},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newCoverRig(t, func(cfg *Config, srv *world.Service) {
				if tc.tweak != nil {
					tc.tweak(srv)
				}
				if tc.want == ReasonTimeout {
					cfg.HTTPClient = &http.Client{Timeout: 40 * time.Millisecond}
				}
			})
			url := tc.url
			if strings.HasPrefix(url, "/") {
				url = rig.srv.URL() + url
			}
			_, err := rig.finder.Cover(context.Background(), ProviderTMDB, "movie:101", url)
			if Reason(err) != tc.want {
				t.Fatalf("Cover = %v, want %s", err, tc.want)
			}
			// A failed download leaves nothing behind, so the card shows its placeholder rather than a broken file.
			entries, readErr := os.ReadDir(filepath.Join(rig.cfg.DataDir, CoverDir))
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatalf("cover dir: %v", readErr)
			}
			if len(entries) != 0 {
				t.Errorf("a failed download left %d file(s)", len(entries))
			}
		})
	}
}

// An id from a provider is data: it may not name a path outside the cover directory.
func TestCoverSanitisesTheID(t *testing.T) {
	rig := newCoverRig(t, nil)
	for _, id := range []string{"../../etc/passwd", "movie:../101", "..", ""} {
		cover, err := rig.finder.Cover(context.Background(), ProviderTMDB, id, rig.srv.URL()+"/cover.png")
		if err != nil {
			t.Fatalf("Cover(%q): %v", id, err)
		}
		if filepath.Dir(cover.Path) != CoverDir {
			t.Errorf("Cover(%q) path = %q, want a file inside %s", id, cover.Path, CoverDir)
		}
		if strings.Contains(cover.Path, "..") {
			t.Errorf("Cover(%q) path = %q", id, cover.Path)
		}
	}
}
