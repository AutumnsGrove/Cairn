package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/AutumnsGrove/cairn/internal/contract"
	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/session"
)

// The session socket: hello, replay, live and replayable events, heartbeat, client messages and their acks. One
// socket is a window onto a session, never the session itself (docs/PROTOCOL.md 3).

const (
	pingInterval  = 20 * time.Second
	readDeadline  = 45 * time.Second
	writeDeadline = 10 * time.Second
	tailInterval  = 50 * time.Millisecond
	// outboundDepth bounds what a slow client can make the service hold. Persisted events wait in the first
	// channel and are never shed; live-only events are shed from the second (docs/STANDARDS.md 4a).
	outboundDepth = 256
	// persistedWait is how long a persisted event may wait for a slow client before the socket is closed, so the
	// client reconnects and replays rather than silently falling behind.
	persistedWait = 5 * time.Second
)

// errorCode is the stable code a refusal carries.
type errorCode string

const (
	codeMalformed   errorCode = "malformed_message"
	codeUnknown     errorCode = "unknown_type"
	codeRateLimit   errorCode = "rate_limited"
	codeQueueFull   errorCode = "queue_full"
	codeClosed      errorCode = "session_closed"
	codeUnavailable errorCode = "unavailable"
	codeInternal    errorCode = "internal"
)

// socket is one upgraded connection.
type socket struct {
	server *Server
	hub    *hub
	ws     *websocket

	sessionID string
	lastSeq   int

	persisted chan []byte
	live      chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	closeCode int
	closeWhy  string
	writeMu   sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// socket serves one connection. It upgrades only after the session and the resume point are known, so an unknown
// id is an ordinary 404 rather than a socket that closes immediately.
func (s *Server) socket(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Cairn is still starting. Try again in a moment.")
		return
	}
	sessionID := r.PathValue("id")
	if len(sessionID) == 0 || len(sessionID) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_value", "That is not a session Cairn knows.")
		return
	}
	lastSeq, err := lastSeqOf(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_value", "That is not a resume point Cairn can use.")
		return
	}
	hello, err := s.engine.Greet(r.Context(), sessionID, lastSeq)
	if err != nil {
		if session.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "There is no such session.")
			return
		}
		if errors.Is(err, session.ErrBadResume) {
			writeError(w, http.StatusBadRequest, "invalid_value", "That is not a resume point Cairn can use.")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}
	first, err := s.hello(r.Context(), hello)
	if err != nil {
		s.log.Error("hello build failed", "session", sessionID, "err", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}

	ws, err := upgrade(w, r)
	if err != nil {
		// The upgrade itself refused; the HTTP status is all a client can see.
		s.log.Debug("upgrade refused", "session", sessionID, "err", err.Error())
		return
	}

	ctx, cancel := context.WithCancel(s.base)
	sock := &socket{
		server: s, hub: s.hub, ws: ws, sessionID: sessionID, lastSeq: hello.HeadSeq,
		persisted: make(chan []byte, outboundDepth), live: make(chan []byte, outboundDepth),
		closed: make(chan struct{}), ctx: ctx, cancel: cancel,
	}
	s.hub.register(sock)
	sock.start(first, hello)
	sock.readLoop()
	sock.close(closeNormal, "the session ended")
	sock.wg.Wait()
	s.hub.unregister(sock)
	cancel()
}

// start writes hello and the replay before anything else can be sent, then starts the three loops.
func (sock *socket) start(first wireEnvelope, hello session.Hello) {
	// The writer is running before hello is queued, so a full replay cannot block this goroutine on a full
	// channel.
	sock.wg.Add(1)
	go sock.writeLoop()
	sock.sendPersistedMustEncode(first)
	for _, ev := range hello.Events {
		data, err := sock.server.wireEvent(sock.ctx, ev)
		if err != nil {
			sock.server.log.Error("replay encode failed", "session", sock.sessionID, "kind", ev.Kind, "err", err.Error())
			continue
		}
		sock.sendPersisted(data)
	}
	sock.hub.subscribe(sock)
	sock.wg.Add(1)
	go sock.tail()
}

// tail follows the persisted log. It is a poll rather than a callback so an event written inside any transaction,
// by the engine or the agent, reaches the socket without either of them knowing a socket exists.
func (sock *socket) tail() {
	defer sock.wg.Done()
	ticker := time.NewTicker(tailInterval)
	defer ticker.Stop()
	for {
		select {
		case <-sock.closed:
			return
		case <-sock.ctx.Done():
			return
		case <-ticker.C:
		}
		events, err := sock.server.engine.EventsSince(sock.ctx, sock.sessionID, sock.lastSeq)
		if err != nil {
			if sock.ctx.Err() == nil {
				sock.server.log.Error("event tail failed", "session", sock.sessionID, "err", err.Error())
			}
			return
		}
		for _, ev := range events {
			data, err := sock.server.wireEvent(sock.ctx, ev)
			if err != nil {
				sock.server.log.Error("event encode failed", "session", sock.sessionID, "kind", ev.Kind, "err", err.Error())
			} else if !sock.sendPersisted(data) {
				return
			}
			sock.lastSeq = ev.Seq
		}
	}
}

// writeLoop is the one writer. A frame is never interleaved with another, which is why every other goroutine
// hands its frames to a channel.
func (sock *socket) writeLoop() {
	defer sock.wg.Done()
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	write := func(opcode byte, payload []byte) bool {
		sock.writeMu.Lock()
		defer sock.writeMu.Unlock()
		_ = sock.ws.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
		return sock.ws.write(opcode, payload) == nil
	}
	for {
		select {
		case data := <-sock.persisted:
			if !write(opText, data) {
				sock.close(closeInternal, "the write failed")
				return
			}
		case data := <-sock.live:
			if !write(opText, data) {
				sock.close(closeInternal, "the write failed")
				return
			}
		case <-ping.C:
			if !write(opPing, nil) {
				sock.close(closeInternal, "the ping failed")
				return
			}
		case <-sock.closed:
			// Everything already queued goes out first, so the ack or snapshot that caused the close is not lost,
			// and the close frame is the last thing written.
			for {
				select {
				case data := <-sock.persisted:
					_ = write(opText, data)
				case data := <-sock.live:
					_ = write(opText, data)
				default:
					_ = write(opClose, closeFrame(sock.closeCode, sock.closeWhy))
					_ = sock.ws.close()
					return
				}
			}
		}
	}
}

// readLoop reads client frames until the socket closes. A protocol failure closes with a stable code.
func (sock *socket) readLoop() {
	var message []byte
	fragmenting := false
	for {
		if err := sock.ws.conn.SetReadDeadline(time.Now().Add(readDeadline)); err != nil {
			return
		}
		f, err := readFrame(sock.ws.br)
		if err != nil {
			sock.server.log.Debug("socket read ended", "session", sock.sessionID, "err", err.Error())
			var wsErr *wsError
			if errors.As(err, &wsErr) {
				if wsErr.code != 0 {
					sock.close(wsErr.code, wsErr.reason)
				} else {
					sock.close(closeNormal, wsErr.reason)
				}
			} else {
				sock.close(closeInternal, "the read failed")
			}
			return
		}
		switch f.opcode {
		case opPing:
			sock.writeControl(opPong, f.payload)
			continue
		case opPong:
			continue
		case opClose:
			sock.close(closeNormal, "the client closed")
			return
		case opBinary:
			sock.close(closeUnsupported, "binary frames are not used")
			return
		case opText:
			if fragmenting {
				sock.close(closeProtocol, "a new message started inside a fragmented one")
				return
			}
			if f.fin {
				sock.handleMessage(f.payload)
				continue
			}
			message = append(message[:0], f.payload...)
			fragmenting = true
		case opContinuation:
			if !fragmenting {
				sock.close(closeProtocol, "a continuation with nothing to continue")
				return
			}
			if len(message)+len(f.payload) > maxFrameBytes {
				sock.close(closeTooBig, "a message over 64 KiB")
				return
			}
			message = append(message, f.payload...)
			if f.fin {
				sock.handleMessage(message)
				message = nil
				fragmenting = false
			}
		}
	}
}

// writeControl writes a control frame, which RFC 6455 allows to interleave with a fragmented message.
func (sock *socket) writeControl(opcode byte, payload []byte) {
	sock.writeMu.Lock()
	defer sock.writeMu.Unlock()
	_ = sock.ws.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	_ = sock.ws.write(opcode, payload)
}

// sendPersisted queues a replayable event and waits for room. A client that cannot keep up is closed so it can
// reconnect and resume from its own last_seq, rather than the service dropping part of the log for it.
func (sock *socket) sendPersisted(data []byte) bool {
	timer := time.NewTimer(persistedWait)
	defer timer.Stop()
	select {
	case sock.persisted <- data:
		return true
	case <-sock.closed:
		return false
	case <-timer.C:
		sock.close(closeRateLimited, "the client is not keeping up")
		return false
	}
}

func (sock *socket) sendPersistedMustEncode(ev wireEnvelope) {
	data, err := json.Marshal(ev)
	if err != nil {
		sock.server.log.Error("hello encode failed", "err", err.Error())
		return
	}
	sock.sendPersisted(data)
}

// sendLive queues a live-only event. When the queue is full the oldest live-only event is shed, because a cost
// tick or an activity label is a moving display and the card and the ledger are the truth.
func (sock *socket) sendLive(data []byte) {
	select {
	case <-sock.closed:
		return
	default:
	}
	select {
	case sock.live <- data:
		return
	default:
	}
	select {
	case <-sock.live:
	default:
	}
	select {
	case sock.live <- data:
	default:
	}
}

// close ends the socket once, with a stable code. The writer sends the close frame and then closes the connection.
func (sock *socket) close(code int, reason string) {
	sock.closeOnce.Do(func() {
		sock.closeCode = code
		sock.closeWhy = reason
		close(sock.closed)
	})
}

// lastSeqOf reads the one query parameter the socket has.
func lastSeqOf(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("last_seq")
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, errors.New("last_seq is not a number")
	}
	return value, nil
}

// --- client messages -------------------------------------------------------------------------------------

// clientMessage is the envelope after the contract has validated it. data stays raw so each type decodes only
// its own fields, strictly.
type clientMessage struct {
	Type        string          `json:"type"`
	ClientMsgID string          `json:"client_msg_id"`
	Data        json.RawMessage `json:"data"`
}

// handleMessage validates one client message against the contract, applies the rate limit, dispatches it and sends
// exactly one ack. Nothing is applied before validation.
func (sock *socket) handleMessage(payload []byte) {
	if !utf8.Valid(payload) {
		sock.close(closeInvalidData, "the message was not UTF-8")
		return
	}
	var msg clientMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		sock.ack("", "rejected", string(codeMalformed))
		return
	}
	// The rate limit runs before the schema check, which is the expensive part, and after the decode so a refusal
	// can quote the id the client will retry with.
	if ok, closeIt := sock.hub.limiter.allow(sock.sessionID); !ok {
		sock.ack(msg.ClientMsgID, "rejected", string(codeRateLimit))
		if closeIt {
			sock.close(closeRateLimited, "too many messages")
		}
		return
	}
	if !contract.KnownClientMessageType(msg.Type) {
		sock.ack(msg.ClientMsgID, "rejected", string(codeUnknown))
		return
	}
	validator, err := contract.Default()
	if err != nil {
		sock.server.log.Error("contract validator", "err", err.Error())
		sock.ack(msg.ClientMsgID, "rejected", string(codeInternal))
		return
	}
	if err := validator.ValidateClientMessage(payload); err != nil {
		// The detail of a schema failure can echo the shape of what she typed, so only the type is logged.
		sock.server.log.Debug("client message did not match the contract", "type", msg.Type)
		sock.ack(msg.ClientMsgID, "rejected", string(codeMalformed))
		return
	}

	ctx := sock.ctx
	switch msg.Type {
	case "message":
		sock.handleUserMessage(ctx, msg)
	case "interaction_response":
		sock.handleInteraction(ctx, msg)
	case "draft_edit":
		sock.handleEdit(ctx, msg)
	case "accept_unverified":
		sock.handleAccept(ctx, msg)
	case "include_left_out":
		sock.handleInclude(ctx, msg)
	case "confirm":
		sock.handleConfirm(ctx, msg)
	case "discard":
		sock.handleDiscard(ctx, msg)
	case "stop":
		sock.hub.stop(sock.sessionID)
		sock.ack(msg.ClientMsgID, "accepted", "")
	case "ping":
		sock.ack(msg.ClientMsgID, "accepted", "")
	default:
		sock.ack(msg.ClientMsgID, "rejected", string(codeUnknown))
	}
}

func (sock *socket) handleUserMessage(ctx context.Context, msg clientMessage) {
	var data struct {
		Text        string   `json:"text"`
		Attachments []string `json:"attachments"`
		ClientDate  string   `json:"client_date"`
		TZ          string   `json:"tz"`
	}
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		sock.ack(msg.ClientMsgID, "rejected", string(codeMalformed))
		return
	}
	if len(data.Attachments) > 0 {
		// Screenshots arrive in S18. Refusing is honest; quietly dropping them would lose something she meant to
		// send.
		sock.ack(msg.ClientMsgID, "rejected", "not_implemented")
		return
	}
	applied, ok := sock.seen(ctx, msg.ClientMsgID)
	if !ok {
		sock.ack(msg.ClientMsgID, "rejected", string(codeInternal))
		return
	}
	if applied {
		sock.ack(msg.ClientMsgID, "duplicate", "")
		return
	}
	err := sock.hub.startTurn(ctx, sock.sessionID, queuedMessage{
		text: data.Text, clientMsgID: msg.ClientMsgID, clientDate: data.ClientDate, tz: data.TZ,
	})
	switch {
	case err == nil:
		sock.ack(msg.ClientMsgID, "accepted", "")
	case errors.Is(err, ErrQueueFull):
		sock.ack(msg.ClientMsgID, "rejected", string(codeQueueFull))
	case errors.Is(err, ErrClosed):
		sock.ack(msg.ClientMsgID, "rejected", string(codeUnavailable))
	default:
		sock.server.log.Warn("turn did not start", "err", err.Error())
		sock.ack(msg.ClientMsgID, "rejected", string(codeInternal))
	}
}

func (sock *socket) handleInteraction(ctx context.Context, msg clientMessage) {
	var data struct {
		InteractionID string          `json:"interaction_id"`
		Value         json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		sock.ack(msg.ClientMsgID, "rejected", string(codeMalformed))
		return
	}
	applied, ok := sock.seen(ctx, msg.ClientMsgID)
	if !ok {
		sock.ack(msg.ClientMsgID, "rejected", string(codeInternal))
		return
	}
	if applied {
		sock.ack(msg.ClientMsgID, "duplicate", "")
		return
	}
	if _, err := sock.server.engine.Answer(ctx, sock.sessionID, data.InteractionID, data.Value, msg.ClientMsgID); err != nil {
		sock.ackRejection(msg.ClientMsgID, err)
		return
	}
	// The answer is stored; the agent waiting on it may now continue.
	sock.hub.responder.deliver(data.InteractionID, data.Value)
	sock.ack(msg.ClientMsgID, "accepted", "")
}

func (sock *socket) handleEdit(ctx context.Context, msg clientMessage) {
	if sock.alreadyApplied(ctx, msg.ClientMsgID) {
		return
	}
	var data struct {
		BaseVersion int                        `json:"base_version"`
		Set         map[string]json.RawMessage `json:"set"`
	}
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		sock.ack(msg.ClientMsgID, "rejected", string(codeMalformed))
		return
	}
	if _, err := sock.server.engine.EditDraft(ctx, sock.sessionID, session.Edit{
		BaseVersion: data.BaseVersion, Set: data.Set, ClientMsgID: msg.ClientMsgID,
	}); err != nil {
		sock.ackDraftRejection(msg.ClientMsgID, err)
		return
	}
	sock.ack(msg.ClientMsgID, "accepted", "")
}

func (sock *socket) handleAccept(ctx context.Context, msg clientMessage) {
	if sock.alreadyApplied(ctx, msg.ClientMsgID) {
		return
	}
	var data struct {
		BaseVersion int      `json:"base_version"`
		Fields      []string `json:"fields"`
		All         bool     `json:"all"`
	}
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		sock.ack(msg.ClientMsgID, "rejected", string(codeMalformed))
		return
	}
	fields := data.Fields
	if data.All {
		card, err := sock.server.engine.Card(ctx, sock.sessionID)
		if err != nil {
			sock.ackDraftRejection(msg.ClientMsgID, err)
			return
		}
		for _, source := range card.Sources {
			if source.Trust == domain.TrustUnverified {
				fields = append(fields, source.Provider+"|"+source.ProviderID)
			}
		}
	}
	if _, err := sock.server.engine.AcceptFields(ctx, sock.sessionID, data.BaseVersion, fields, msg.ClientMsgID); err != nil {
		sock.ackDraftRejection(msg.ClientMsgID, err)
		return
	}
	sock.ack(msg.ClientMsgID, "accepted", "")
}

func (sock *socket) handleInclude(ctx context.Context, msg clientMessage) {
	if sock.alreadyApplied(ctx, msg.ClientMsgID) {
		return
	}
	var data struct {
		BaseVersion int    `json:"base_version"`
		SegmentID   string `json:"segment_id"`
	}
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		sock.ack(msg.ClientMsgID, "rejected", string(codeMalformed))
		return
	}
	span, ok := parseSegmentID(data.SegmentID)
	if !ok {
		sock.ack(msg.ClientMsgID, "rejected", string(codeMalformed))
		return
	}
	source, err := sock.server.engine.SourceText(ctx, sock.sessionID)
	if err != nil {
		sock.ackDraftRejection(msg.ClientMsgID, err)
		return
	}
	if span.End > len(source) {
		sock.ack(msg.ClientMsgID, "rejected", string(codeMalformed))
		return
	}
	span.Text = source[span.Start:span.End]
	if _, err := sock.server.engine.IncludeLeftOut(ctx, sock.sessionID, data.BaseVersion, source, span,
		msg.ClientMsgID); err != nil {
		sock.ackDraftRejection(msg.ClientMsgID, err)
		return
	}
	sock.ack(msg.ClientMsgID, "accepted", "")
}

func (sock *socket) handleConfirm(ctx context.Context, msg clientMessage) {
	if sock.alreadyApplied(ctx, msg.ClientMsgID) {
		return
	}
	var data struct {
		BaseVersion int `json:"base_version"`
	}
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		sock.ack(msg.ClientMsgID, "rejected", string(codeMalformed))
		return
	}
	source, err := sock.server.engine.SourceText(ctx, sock.sessionID)
	if err != nil {
		sock.ackDraftRejection(msg.ClientMsgID, err)
		return
	}
	_, _, _, err = sock.server.engine.Confirm(ctx, sock.sessionID, session.ConfirmRequest{
		BaseVersion: data.BaseVersion, ClientMsgID: msg.ClientMsgID, SourceText: source,
		Revises: sock.hub.getRevises(sock.sessionID),
	})
	if err != nil {
		sock.ackDraftRejection(msg.ClientMsgID, err)
		return
	}
	sock.hub.finishTurn(sock.sessionID)
	sock.ack(msg.ClientMsgID, "accepted", "")
}

func (sock *socket) handleDiscard(ctx context.Context, msg clientMessage) {
	if sock.alreadyApplied(ctx, msg.ClientMsgID) {
		return
	}
	if err := sock.server.engine.Discard(ctx, sock.sessionID, msg.ClientMsgID); err != nil {
		if session.IsDuplicate(err) {
			sock.ack(msg.ClientMsgID, "duplicate", "")
			return
		}
		sock.ackRejection(msg.ClientMsgID, err)
		return
	}
	sock.hub.finishTurn(sock.sessionID)
	sock.ack(msg.ClientMsgID, "accepted", "")
}

// seen reports whether a client_msg_id was already applied, and whether the check itself worked.
func (sock *socket) seen(ctx context.Context, clientMsgID string) (applied, ok bool) {
	if clientMsgID == "" {
		return false, true
	}
	_, found, err := sock.server.engine.Seen(ctx, sock.sessionID, clientMsgID)
	if err != nil {
		sock.server.log.Error("client message lookup failed", "session", sock.sessionID, "id", clientMsgID, "err", err.Error())
		return false, false
	}
	return found, true
}

// alreadyApplied answers a repeat of a client message with the first outcome and reports true, so a stale version on
// the retry cannot be reported as a new error. A duplicate changes nothing (docs/PROTOCOL.md 6).
func (sock *socket) alreadyApplied(ctx context.Context, clientMsgID string) bool {
	applied, ok := sock.seen(ctx, clientMsgID)
	if !ok {
		sock.ack(clientMsgID, "rejected", string(codeInternal))
		return true
	}
	if applied {
		sock.ack(clientMsgID, "duplicate", "")
	}
	return applied
}

// ack is the reply to one client message. It is live-only and carries a stable status, never any of her text.
func (sock *socket) ack(clientMsgID, status, reason string) {
	if clientMsgID == "" && status == "rejected" {
		// A message that could not even be decoded has no id to quote; the socket still answers, with the
		// contract's id shape. The client sees the close or the rejection and retries.
		clientMsgID = "unknown"
	}
	data, err := encodeEnvelope(nil, "ack", sock.sessionID, sock.server.now(), map[string]any{
		"client_msg_id": clientMsgID, "status": status, "reason": reason,
	})
	if err != nil {
		return
	}
	sock.sendLive(data)
}

// ackRejection answers with the engine's stable code. A refusal the registry knows is passed through; anything else
// is an internal failure and is logged, not described.
func (sock *socket) ackRejection(clientMsgID string, err error) {
	if session.IsDuplicate(err) {
		sock.ack(clientMsgID, "duplicate", "")
		return
	}
	if errors.Is(err, session.ErrClosed) {
		sock.ack(clientMsgID, "rejected", string(codeClosed))
		return
	}
	if errors.Is(err, session.ErrNoDraft) {
		sock.ack(clientMsgID, "rejected", "not_a_draft")
		return
	}
	if code, _, ok := refusalOf(err); ok {
		sock.ack(clientMsgID, "rejected", code)
		return
	}
	sock.server.log.Error("client message failed", "session", sock.sessionID, "err", err.Error())
	sock.ack(clientMsgID, "rejected", string(codeInternal))
}

// ackDraftRejection adds the one thing a version conflict owes the client: the current card, so it can redraw
// without a reconnect (docs/PROTOCOL.md 5).
func (sock *socket) ackDraftRejection(clientMsgID string, err error) {
	code, _, ok := refusalOf(err)
	if ok && code == "stale_version" {
		sock.ack(clientMsgID, "rejected", code)
		sock.pushSnapshot()
		return
	}
	sock.ackRejection(clientMsgID, err)
}

// pushSnapshot sends the current card with no sequence number. It is the answer to a stale version, not a new log
// entry.
func (sock *socket) pushSnapshot() {
	card, err := sock.server.engine.Card(sock.ctx, sock.sessionID)
	if err != nil {
		return
	}
	wire := sock.server.wireCardOf(card)
	data, err := encodeEnvelope(nil, "draft_snapshot", sock.sessionID, sock.server.now(), map[string]any{
		"version": card.Draft.Version, "draft": wire,
	})
	if err == nil {
		sock.sendLive(data)
	}
}

// refusalOf lifts the stable code out of the two refusal types the engine and the domain return.
func refusalOf(err error) (code, message string, ok bool) {
	var refusal *session.Refusal
	if errors.As(err, &refusal) {
		return refusal.Code, refusal.Message, true
	}
	var guard *domain.GuardError
	if errors.As(err, &guard) {
		return guard.Code, guard.Message, true
	}
	return "", "", false
}
