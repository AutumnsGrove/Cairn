package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/AutumnsGrove/cairn/internal/contract"
	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/session"
)

// The REST half from api/openapi.yaml: the pages that refresh, the lists, and the actions that must survive a
// reload. Every one sits behind the same guard as the socket, carries the same envelope on failure, and is paged
// with an opaque cursor, never an offset (docs/STANDARDS.md 4).

// Limits from docs/STANDARDS.md 4a. A body over maxJSONBody is refused, and a list page is capped rather than
// quietly shortened.
const (
	maxJSONBody  = 64 << 10
	defaultLimit = 50
	maxLimit     = 100
)

// writeJSON is the one success shape: JSON, no cache, and the status the contract names.
func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong. Try again.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// needEngine answers the "the service is still starting" state without a panic.
func (s *Server) needEngine(w http.ResponseWriter) bool {
	if s.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Cairn is still starting. Try again in a moment.")
		return false
	}
	return true
}

// decodeJSON reads one bounded JSON body strictly. A wrong content type, a body over the limit, an unknown field
// and trailing data are all refused with their own stable code.
func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	if strings.TrimSpace(mediaType) != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "bad_content_type", "Send that as JSON.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody+1)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "That is too big to send.")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_value", "That request could not be read.")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_value", "That request had more than one object in it.")
		return false
	}
	return true
}

// queryLimit reads ?limit, caps it and refuses a value that is not a number.
func queryLimit(r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultLimit, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, false
	}
	if value > maxLimit {
		value = maxLimit
	}
	return value, true
}

// encodeCursor packs the parts of a cursor into one opaque string.
func encodeCursor(parts ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, "\x1f")))
}

func decodeCursor(raw string) ([]string, bool) {
	if raw == "" {
		return nil, true
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, false
	}
	parts := strings.Split(string(decoded), "\x1f")
	for _, part := range parts {
		if strings.ContainsRune(part, 0) {
			return nil, false
		}
	}
	return parts, true
}

// restError maps an engine or store failure to the contract's code and status. An unrecognised failure is internal
// and logged, never described to the client.
func (s *Server) restError(w http.ResponseWriter, err error) {
	if session.IsNotFound(err) {
		writeError(w, http.StatusNotFound, "not_found", "There is nothing here.")
		return
	}
	if errors.Is(err, session.ErrClosed) {
		writeError(w, http.StatusConflict, "session_closed", "This conversation is finished. Start a new one.")
		return
	}
	if code, message, ok := refusalOf(err); ok {
		status := contract.Status(contract.ErrorCode(code))
		writeError(w, status, code, message)
		return
	}
	s.log.Error("request failed", "err", err.Error())
	writeError(w, http.StatusInternalServerError, "internal", "Something went wrong. Try again.")
}

// --- sessions -------------------------------------------------------------------------------------------

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	if !s.needEngine(w) {
		return
	}
	if r.Method == http.MethodPost {
		s.createSession(w, r)
		return
	}
	s.listSessions(w, r)
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind  string `json:"kind"`
		Title string `json:"title"`
	}
	if !s.decodeJSON(w, r, &body) {
		return
	}
	if len(body.Title) > 300 {
		writeError(w, http.StatusBadRequest, "invalid_value", "That title is too long.")
		return
	}
	ctx := r.Context()
	var (
		sess domain.Session
		err  error
	)
	switch domain.SessionKind(body.Kind) {
	case domain.SessionEntry:
		sess, err = s.engine.CreateEntry(ctx)
	case domain.SessionGeneral:
		sess, err = s.engine.CreateGeneral(ctx, body.Title)
	default:
		writeError(w, http.StatusBadRequest, "invalid_value", "A session is either an entry or a chat.")
		return
	}
	if err != nil {
		s.restError(w, err)
		return
	}
	waiting := ""
	if sess.Kind == domain.SessionEntry {
		waiting = string(session.WaitingPickWork)
	}
	writeJSON(w, http.StatusCreated, wireSessionOf(sess, waiting))
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	state := domain.SessionState(query.Get("state"))
	kind := domain.SessionKind(query.Get("kind"))
	switch state {
	case "", domain.SessionActive, domain.SessionSaved, domain.SessionDiscarded:
	default:
		writeError(w, http.StatusBadRequest, "invalid_value", "That is not a session state.")
		return
	}
	switch kind {
	case "", domain.SessionEntry, domain.SessionGeneral:
	default:
		writeError(w, http.StatusBadRequest, "invalid_value", "That is not a kind of session.")
		return
	}
	limit, ok := queryLimit(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_value", "That is not a page size.")
		return
	}
	parts, ok := decodeCursor(query.Get("cursor"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_value", "That is not a cursor Cairn gave you.")
		return
	}
	before := ""
	if len(parts) > 0 {
		before = strings.Join(parts, "\x1f")
	}
	ctx := r.Context()
	sessions, err := s.engine.Sessions(ctx, state, kind, before, limit+1)
	if err != nil {
		s.restError(w, err)
		return
	}
	var next *string
	if len(sessions) > limit {
		sessions = sessions[:limit]
		last := sessions[len(sessions)-1]
		cursor := encodeCursor(instant(last.UpdatedAt) + "|" + last.ID)
		next = &cursor
	}
	items := make([]wireSession, 0, len(sessions))
	for _, sess := range sessions {
		waiting := ""
		if sess.Kind == domain.SessionEntry {
			reason, waitErr := s.engine.Waiting(ctx, sess.ID)
			if waitErr != nil {
				s.restError(w, waitErr)
				return
			}
			waiting = string(reason)
		}
		items = append(items, wireSessionOf(sess, waiting))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) serveSession(w http.ResponseWriter, r *http.Request) {
	if !s.needEngine(w) {
		return
	}
	id := r.PathValue("id")
	sess, err := s.engine.Session(r.Context(), id)
	if err != nil {
		s.restError(w, err)
		return
	}
	waiting := ""
	if sess.Kind == domain.SessionEntry {
		reason, waitErr := s.engine.Waiting(r.Context(), sess.ID)
		if waitErr != nil {
			s.restError(w, waitErr)
			return
		}
		waiting = string(reason)
	}
	writeJSON(w, http.StatusOK, wireSessionOf(sess, waiting))
}

// uploadMedia is S18. Refusing with a stable code is honest; a 201 with nowhere to put the file is not.
func (s *Server) uploadMedia(w http.ResponseWriter, r *http.Request) {
	if !s.needEngine(w) {
		return
	}
	if _, err := s.engine.Session(r.Context(), r.PathValue("id")); err != nil {
		s.restError(w, err)
		return
	}
	s.notBuilt(w, r)
}

// --- entries --------------------------------------------------------------------------------------------

func (s *Server) listEntries(w http.ResponseWriter, r *http.Request) {
	if !s.needEngine(w) {
		return
	}
	query := r.URL.Query()
	medium := query.Get("medium")
	if medium != "" {
		switch medium {
		case "film", "tv", "book", "game", "album", "podcast", "theatre", "other":
		default:
			writeError(w, http.StatusBadRequest, "invalid_value", "That is not a medium Cairn knows.")
			return
		}
	}
	var favourite *bool
	if raw := query.Get("favourite"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_value", "favourite is true or false.")
			return
		}
		favourite = &value
	}
	limit, ok := queryLimit(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_value", "That is not a page size.")
		return
	}
	parts, ok := decodeCursor(query.Get("cursor"))
	if !ok || len(parts) > 2 {
		writeError(w, http.StatusBadRequest, "invalid_value", "That is not a cursor Cairn gave you.")
		return
	}
	cursor := domain.Cursor{}
	if len(parts) == 2 {
		cursor = domain.Cursor{FinishedOn: parts[0], ID: parts[1]}
	}
	ctx := r.Context()
	entries, next, err := s.engine.Library(ctx, medium, favourite, cursor, limit)
	if err != nil {
		s.restError(w, err)
		return
	}
	items := make([]wireEntry, 0, len(entries))
	for _, entry := range entries {
		view, viewErr := s.engine.EntryView(ctx, entry.ID)
		if viewErr != nil {
			s.restError(w, viewErr)
			return
		}
		items = append(items, wireEntryOf(view))
	}
	var cursorOut *string
	if !next.Empty() {
		encoded := encodeCursor(next.FinishedOn, next.ID)
		cursorOut = &encoded
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": cursorOut})
}

func (s *Server) serveEntry(w http.ResponseWriter, r *http.Request) {
	if !s.needEngine(w) {
		return
	}
	view, err := s.engine.EntryView(r.Context(), r.PathValue("id"))
	if err != nil {
		s.restError(w, err)
		return
	}
	// The REST entry is the library's: a draft is reached through its session and the socket, and its rating may
	// be zero, which the entry contract does not allow.
	if view.Entry.Status != domain.StatusSaved {
		writeError(w, http.StatusNotFound, "not_found", "There is nothing here.")
		return
	}
	writeJSON(w, http.StatusOK, wireEntryOf(view))
}

func (s *Server) listRevisions(w http.ResponseWriter, r *http.Request) {
	if !s.needEngine(w) {
		return
	}
	entryID := r.PathValue("id")
	limit, ok := queryLimit(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_value", "That is not a page size.")
		return
	}
	parts, ok := decodeCursor(r.URL.Query().Get("cursor"))
	if !ok || len(parts) > 1 {
		writeError(w, http.StatusBadRequest, "invalid_value", "That is not a cursor Cairn gave you.")
		return
	}
	before := 0
	if len(parts) == 1 {
		before, _ = strconv.Atoi(parts[0])
	}
	ctx := r.Context()
	view, err := s.engine.EntryView(ctx, entryID)
	if err != nil {
		s.restError(w, err)
		return
	}
	revisions, next, err := s.engine.RevisionsPage(ctx, entryID, before, limit)
	if err != nil {
		s.restError(w, err)
		return
	}
	items := make([]wireRevision, 0, len(revisions))
	for _, revision := range revisions {
		items = append(items, wireRevisionOf(revision, view.Work, view.Answers))
	}
	var cursorOut *string
	if next > 0 {
		encoded := encodeCursor(strconv.Itoa(next))
		cursorOut = &encoded
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": cursorOut})
}

func (s *Server) deleteEntry(w http.ResponseWriter, r *http.Request) {
	if !s.needEngine(w) {
		return
	}
	ctx := r.Context()
	if _, err := s.engine.EntryView(ctx, r.PathValue("id")); err != nil {
		s.restError(w, err)
		return
	}
	if err := s.engine.DeleteEntry(ctx, r.PathValue("id")); err != nil {
		s.restError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- works, search, export, version and services -----------------------------------------------------------

func (s *Server) serveWork(w http.ResponseWriter, r *http.Request) {
	if !s.needEngine(w) {
		return
	}
	view, err := s.engine.WorkView(r.Context(), r.PathValue("id"))
	if err != nil {
		s.restError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wireWorkDetailOf(view))
}

// search is S16. The route exists so a frontend can build against it, and refuses plainly until then.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	if !s.needEngine(w) {
		return
	}
	query := r.URL.Query().Get("q")
	if strings.TrimSpace(query) == "" {
		writeError(w, http.StatusBadRequest, "invalid_value", "Type something to search for.")
		return
	}
	if len(query) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_value", "That search is too long.")
		return
	}
	s.notBuilt(w, r)
}

func (s *Server) exportStub(w http.ResponseWriter, r *http.Request) {
	if !s.needEngine(w) {
		return
	}
	s.notBuilt(w, r)
}

func (s *Server) serveVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    versionString(),
		"deployment": "bare",
		"updater":    map[string]any{"state": "not_installed", "heartbeat_at": nil},
	})
}

// serveServices reports what is configured. It never makes a live call: a screen that needs one answers slowly,
// and this is the screen that explains why something is slow.
func (s *Server) serveServices(w http.ResponseWriter, r *http.Request) {
	state := func(configured bool) string {
		if configured {
			return "ok"
		}
		return "not_configured"
	}
	services := []map[string]any{
		{"name": "database", "state": state(s.engine != nil)},
		{"name": "decisions", "state": state(s.model != nil)},
		{"name": "polaris", "state": "not_configured"},
		{"name": "tmdb", "state": state(s.cfg.TMDBKey != "")},
		{"name": "open_library", "state": "ok"},
		{"name": "r2", "state": "not_configured"},
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": services})
}
