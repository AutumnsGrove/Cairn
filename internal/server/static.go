package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// maxAssetSize bounds a single embedded file. The build is small; anything larger is a mistake, and reading
// it whole would be an unbounded read of something we control least (docs/STANDARDS.md 4a).
const maxAssetSize = 8 << 20

// placeholderHTML is served when the frontend has not been built, so a fresh clone still starts and answers.
// It carries no script at all and its own tight CSP: it exists to say one sentence, not to be an app.
const placeholderHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Cairn</title>
<style>
  :root { color-scheme: dark; }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: oklch(0.175 0.008 70); color: oklch(0.91 0.012 82); font: 400 1rem/1.6 system-ui, sans-serif; text-align: center; padding: 2rem; }
  h1 { font-size: 1.5rem; margin: 0 0 .5rem; }
  p { margin: 0; color: oklch(0.74 0.012 78); max-width: 32ch; }
  code { font-family: ui-monospace, monospace; }
</style>
</head>
<body>
<h1>Cairn</h1>
<p>The frontend has not been built yet. Run <code>make web</code>, then restart.</p>
</body>
</html>
`

// static serves the embedded frontend. A path that does not exist is either a SPA route, which gets
// index.html, or a missing asset, which gets the envelope.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if rel == "" || rel == "." {
		rel = "index.html"
	}

	data, name, ok := s.readAsset(rel)
	if !ok {
		// A missing file with an extension is a missing asset, not a route. The root is the exception: a
		// missing index.html means the frontend has not been built, which is a placeholder, not a 404.
		if rel != "index.html" && path.Ext(rel) != "" {
			writeError(w, http.StatusNotFound, "not_found", "No such file.")
			return
		}
		data, name, ok = s.readAsset("index.html")
	}
	if !ok {
		s.servePlaceholder(w, r)
		return
	}
	s.serveAsset(w, r, name, data)
}

// readAsset reads one file, following a directory to its index.html. It reports whether the file exists.
func (s *Server) readAsset(rel string) ([]byte, string, bool) {
	for _, candidate := range []string{rel, path.Join(rel, "index.html")} {
		f, err := s.assets.Open(candidate)
		if err != nil {
			continue
		}
		info, statErr := f.Stat()
		if statErr == nil && info.IsDir() {
			_ = f.Close()
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxAssetSize+1))
		_ = f.Close()
		if readErr != nil || len(data) > maxAssetSize {
			return nil, "", false
		}
		return data, candidate, true
	}
	return nil, "", false
}

func (s *Server) servePlaceholder(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'self'; base-uri 'none'; form-action 'none'; script-src 'none'; frame-ancestors 'none'")
	h.Set("ETag", etagOf([]byte(placeholderHTML)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, placeholderHTML)
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name string, identity []byte) {
	h := w.Header()
	h.Set("Content-Type", contentType(name))
	h.Set("Cache-Control", cacheControlFor(name))
	// The reply depends on what the client can decode, so a shared cache must key on that.
	h.Add("Vary", "Accept-Encoding")

	body, encoding := s.encoded(name, identity, r.Header.Get("Accept-Encoding"))
	if encoding != "" {
		h.Set("Content-Encoding", encoding)
	}
	// The entity tag names the encoding too: a brotli copy and a gzip copy are different bodies.
	etag := etagOf(identity)
	if encoding != "" {
		etag = strings.TrimSuffix(etag, `"`) + "-" + encoding + `"`
	}
	h.Set("ETag", etag)

	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		h.Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// encoded picks the precompressed copy the build wrote, if the client asked for one. Nothing is compressed at
// run time: adapter-static wrote the .br and .gz beside the file, and they are served as they are.
func (s *Server) encoded(name string, identity []byte, acceptEncoding string) ([]byte, string) {
	for _, candidate := range []struct{ encoding, suffix string }{{"br", ".br"}, {"gzip", ".gz"}} {
		if !acceptsEncoding(acceptEncoding, candidate.encoding) {
			continue
		}
		data, _, ok := s.readAsset(name + candidate.suffix)
		if ok {
			return data, candidate.encoding
		}
	}
	return identity, ""
}

// acceptsEncoding is deliberately simple: a token present and not explicitly refused with q=0. No client we
// serve sends anything subtler, and a wrong guess only costs a larger body.
func acceptsEncoding(header, encoding string) bool {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(fields[0]), encoding) {
			continue
		}
		for _, param := range fields[1:] {
			if strings.TrimSpace(param) == "q=0" {
				return false
			}
		}
		return true
	}
	return false
}

// cacheControlFor gives a hashed build asset a year and everything else a revalidation. SvelteKit writes
// content-hashed files under _app/immutable, so a year is safe there and wrong anywhere else.
func cacheControlFor(name string) string {
	if strings.HasPrefix(name, "_app/immutable/") || strings.HasPrefix(name, "_app/") {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
}

func contentType(name string) string {
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	switch path.Ext(name) {
	case ".woff2":
		return "font/woff2"
	case ".webmanifest":
		return "application/manifest+json"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func etagOf(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == etag || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}
