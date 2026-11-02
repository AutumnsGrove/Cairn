package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	// The three formats we size with image.DecodeConfig. A format we cannot size cannot be bounded, so it is
	// refused rather than stored.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// The cover limits from docs/STANDARDS.md 4a, and the freshness rule from G3.
const (
	// MaxCoverBytes bounds one cover download.
	MaxCoverBytes = 2 << 20
	// MaxCoverSide bounds either dimension; a decompression bomb is refused before it is written.
	MaxCoverSide = 4_000
	// CoverMaxAge is the TMDB terms' six-month caching cap, made a little stricter. A cover older than this is
	// fetched again the next time it is used, so freshness needs no background job (G3, decisions 2).
	CoverMaxAge = 180 * 24 * time.Hour
)

// coverTypes are the content types a cover may have, with the extension each is stored under.
var coverTypes = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/gif":  "gif",
}

// Cover is a stored cover: a path relative to the data directory, and what it is.
type Cover struct {
	Path        string
	ContentType string
	Bytes       int
	FetchedAt   time.Time
}

// Cover keeps a local copy of a cover, fetching it only when there is no fresh one.
//
// Nothing here trusts the URL: it must be https (or the loopback the tests use), its host must be one the config
// names, and the bytes must be an image whose dimensions can be read. A failed download writes nothing, so the
// card falls back to its placeholder rather than to a broken file.
func (f *Finder) Cover(ctx context.Context, provider, id, coverURL string) (Cover, error) {
	if cover, ok := f.freshCover(provider, id); ok {
		return cover, nil
	}
	if strings.TrimSpace(coverURL) == "" {
		return Cover{}, &Error{Provider: provider, Op: "cover", Reason: ReasonNotFound, Err: errors.New("no cover")}
	}
	parsed, err := url.Parse(coverURL)
	if err != nil || parsed.Host == "" {
		return Cover{}, &Error{Provider: provider, Op: "cover", Reason: ReasonUnavailable, Err: errors.New("cover URL is not a URL")}
	}
	if parsed.Scheme != "https" && !f.loopbackHost(parsed.Hostname()) {
		return Cover{}, &Error{
			Provider: provider, Op: "cover", Reason: ReasonUnavailable,
			Err: fmt.Errorf("cover host %q is not allowed", parsed.Hostname()),
		}
	}
	if !f.allowedCover(parsed.Hostname()) {
		return Cover{}, &Error{
			Provider: provider, Op: "cover", Reason: ReasonUnauthorized,
			Err: fmt.Errorf("cover host %q is not one the config names", parsed.Hostname()),
		}
	}

	if waitErr := f.covers.limiter.wait(ctx); waitErr != nil {
		return Cover{}, f.covers.failure(ctx, "cover", waitErr)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Cover{}, &Error{Provider: provider, Op: "cover", Reason: ReasonUnavailable, Err: err}
	}
	req.Header.Set("User-Agent", f.covers.userAgent)
	req.Header.Set("Accept", "image/*")
	resp, err := f.covers.http.Do(req)
	if err != nil {
		return Cover{}, f.covers.failure(ctx, "cover", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Cover{}, &Error{Provider: provider, Op: "cover", Reason: ReasonNotFound, Status: resp.StatusCode}
	case resp.StatusCode != http.StatusOK:
		return Cover{}, f.covers.failure(ctx, "cover", fmt.Errorf("cover download answered %d", resp.StatusCode))
	}

	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	ext, ok := coverTypes[contentType]
	if !ok {
		return Cover{}, &Error{
			Provider: provider, Op: "cover", Reason: ReasonNotAnImage,
			Err: fmt.Errorf("cover content type %q", contentType),
		}
	}
	if resp.ContentLength > MaxCoverBytes {
		return Cover{}, &Error{
			Provider: provider, Op: "cover", Reason: ReasonCoverTooBig,
			Err: fmt.Errorf("cover is %d bytes", resp.ContentLength),
		}
	}

	body, err := readCoverBody(resp.Body)
	if err != nil {
		if errors.Is(err, errTooLarge) {
			return Cover{}, &Error{Provider: provider, Op: "cover", Reason: ReasonCoverTooBig, Err: err}
		}
		return Cover{}, f.covers.failure(ctx, "cover", err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return Cover{}, &Error{Provider: provider, Op: "cover", Reason: ReasonNotAnImage, Err: err}
	}
	if config.Width > MaxCoverSide || config.Height > MaxCoverSide {
		return Cover{}, &Error{
			Provider: provider, Op: "cover", Reason: ReasonCoverTooBig,
			Err: fmt.Errorf("cover is %dx%d", config.Width, config.Height),
		}
	}

	stored, err := f.writeCover(provider, id, ext, body)
	if err != nil {
		return Cover{}, &Error{Provider: provider, Op: "cover", Reason: ReasonUnavailable, Err: err}
	}
	return Cover{
		Path:        stored,
		ContentType: contentType,
		Bytes:       len(body),
		FetchedAt:   f.clock.Now(),
	}, nil
}

// CoverDir is where covers live inside the data directory. It is the one place that name appears.
const CoverDir = "covers"

// coverName builds the stored file name: provider, a sanitised id and the extension. The id is reduced to
// letters, digits, dash and underscore, with every other run collapsed to one dash, so an id from a provider can
// never name a path outside the cover directory and never produces a name with a dot in it.
func coverName(provider, id, ext string) string {
	var b strings.Builder
	b.Grow(len(id))
	lastDash := false
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	name := provider + "-" + strings.Trim(b.String(), "-_")
	if name == provider+"-" {
		name = provider + "-unnamed"
	}
	if ext != "" {
		name += "." + ext
	}
	return name
}

// freshCover finds an existing cover that is still inside the freshness window.
func (f *Finder) freshCover(provider, id string) (Cover, bool) {
	dir := filepath.Join(f.dataDir, CoverDir)
	for contentType, ext := range coverTypes {
		full := filepath.Join(dir, coverName(provider, id, ext))
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue
		}
		if f.clock.Now().Sub(info.ModTime()) >= CoverMaxAge {
			continue
		}
		return Cover{
			Path:        path.Join(CoverDir, coverName(provider, id, ext)),
			ContentType: contentType,
			Bytes:       int(info.Size()),
			FetchedAt:   info.ModTime(),
		}, true
	}
	return Cover{}, false
}

// writeCover stores the bytes atomically, so a crash leaves either the old cover or the new one, never half of a
// file. The modification time is the fetch time, which is what the freshness rule reads.
func (f *Finder) writeCover(provider, id, ext string, body []byte) (string, error) {
	dir := filepath.Join(f.dataDir, CoverDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	name := coverName(provider, id, ext)
	temp, err := os.CreateTemp(dir, ".cover-*")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
	}()
	if _, err := temp.Write(body); err != nil {
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	full := filepath.Join(dir, name)
	if err := os.Rename(temp.Name(), full); err != nil {
		return "", err
	}
	// The freshness rule reads the file's own modification time, so set it from the injected clock instead of
	// leaving it to the machine's: a test can then move time, and a clock skew cannot quietly extend the cache.
	now := f.clock.Now()
	if err := os.Chtimes(full, now, now); err != nil {
		return "", err
	}
	return path.Join(CoverDir, name), nil
}

// readCoverBody reads at most the cover limit and refuses one byte more.
func readCoverBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxCoverBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxCoverBytes {
		return nil, errTooLarge
	}
	return body, nil
}

// loopbackHost reports whether a host is this machine, which is what the world's https-less URL is.
func (f *Finder) loopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// allowedCover reports whether a host is one the config named, or loopback for a test.
func (f *Finder) allowedCover(host string) bool {
	if f.loopbackHost(host) {
		return true
	}
	_, ok := f.coverHosts[host]
	return ok
}

// coverHostsFrom reads the hosts of the configured cover bases.
func coverHostsFrom(bases ...string) map[string]bool {
	out := make(map[string]bool, len(bases))
	for _, base := range bases {
		parsed, err := url.Parse(base)
		if err != nil || parsed.Hostname() == "" {
			continue
		}
		out[parsed.Hostname()] = true
	}
	return out
}
