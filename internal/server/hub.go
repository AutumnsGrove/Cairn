package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/AutumnsGrove/cairn/internal/agent"
)

// The hub owns everything that outlives one connection: the per-session turn, the one-deep queue, the idempotency
// of a question waiting for her tap, the rate limit and the set of sockets. A turn does not depend on a socket
// (docs/PROTOCOL.md 1), so it is started and cancelled here rather than in a request.

// ErrQueueFull means one message is already waiting behind the turn.
var ErrQueueFull = errors.New("server: one message is already waiting")

// ErrClosed means the service is going away and will not start a turn.
var ErrClosed = errors.New("server: the service is closing")

// queuedMessage is a message the client sent while a turn was running. It lives in memory with the turn, because
// a restart ends the turn and the client retries from its outbox with the same client_msg_id.
type queuedMessage struct {
	text        string
	clientMsgID string
	clientDate  string
	tz          string
}

// turn is the one turn a session may have in flight, and whatever is waiting behind it.
type turn struct {
	cancel  context.CancelFunc
	revises string
	queued  *queuedMessage
}

// hub is the per-service state the sockets share.
type hub struct {
	server *Server

	mu        sync.Mutex
	conns     map[*socket]struct{}
	bySession map[string]map[*socket]struct{}
	turns     map[string]*turn
	closing   bool

	limiter   *rateLimiter
	responder *Responder
	// turns counts running turn goroutines so a drain can wait for them.
	turnsWG sync.WaitGroup
}

func newHub(s *Server) *hub {
	responder := s.responder
	if responder == nil {
		responder = NewResponder()
	}
	return &hub{
		server: s, conns: map[*socket]struct{}{}, bySession: map[string]map[*socket]struct{}{},
		turns: map[string]*turn{}, limiter: newRateLimiter(s.now), responder: responder,
	}
}

func (h *hub) register(sock *socket) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[sock] = struct{}{}
}

func (h *hub) unregister(sock *socket) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, sock)
	if subs, ok := h.bySession[sock.sessionID]; ok {
		delete(subs, sock)
		if len(subs) == 0 {
			delete(h.bySession, sock.sessionID)
		}
	}
}

// subscribe adds a socket to its session after hello has been sent, so it never receives an event twice.
func (h *hub) subscribe(sock *socket) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.bySession[sock.sessionID] == nil {
		h.bySession[sock.sessionID] = map[*socket]struct{}{}
	}
	h.bySession[sock.sessionID][sock] = struct{}{}
}

// publishLive sends a live-only event to every socket on a session. A live event is shed rather than waited for:
// the draft, the log and the ledger are the truth, and cost and activity are a moving display.
func (h *hub) publishLive(ev agent.Event) {
	data, err := h.server.wireLive(ev)
	if err != nil {
		h.server.log.Error("live event encode failed", "kind", ev.Kind, "err", err.Error())
		return
	}
	h.mu.Lock()
	targets := make([]*socket, 0, len(h.bySession[ev.SessionID]))
	for sock := range h.bySession[ev.SessionID] {
		targets = append(targets, sock)
	}
	h.mu.Unlock()
	for _, sock := range targets {
		sock.sendLive(data)
	}
}

// publishSession sends a live-only event to one session. It is used for acks that concern every device, such as a
// queue change.
func (h *hub) publishSession(sessionID string, data []byte) {
	h.mu.Lock()
	targets := make([]*socket, 0, len(h.bySession[sessionID]))
	for sock := range h.bySession[sessionID] {
		targets = append(targets, sock)
	}
	h.mu.Unlock()
	for _, sock := range targets {
		sock.sendLive(data)
	}
}

// closeAll tells every socket the service is going away. The close code is stable, so a client can tell a
// restart from a network drop.
func (h *hub) closeAll(code int, reason string) {
	h.mu.Lock()
	h.closing = true
	targets := make([]*socket, 0, len(h.conns))
	for sock := range h.conns {
		targets = append(targets, sock)
	}
	// Cancel a running turn only after the grace period, not here: the drain waits for it to finish.
	h.mu.Unlock()
	for _, sock := range targets {
		sock.close(code, reason)
	}
}

// startTurn runs a message, or queues it behind the one in flight. The caller has already made the message the
// agent's, so an error means nothing started.
func (h *hub) startTurn(ctx context.Context, sessionID string, msg queuedMessage) error {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return ErrClosed
	}
	if existing, ok := h.turns[sessionID]; ok {
		if existing.queued != nil {
			h.mu.Unlock()
			return ErrQueueFull
		}
		existing.queued = &msg
		h.mu.Unlock()
		h.publishQueueState(sessionID, 1)
		return nil
	}
	// The socket's cancellation is stripped: a turn outlives the connection that asked for it. Values (a request
	// id, if one is ever put there) are kept.
	turnCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	state := &turn{cancel: cancel}
	h.turns[sessionID] = state
	h.turnsWG.Add(1)
	h.mu.Unlock()
	go h.runTurn(turnCtx, sessionID, msg)
	return nil
}

// runTurn drives one message through the agent and then takes the next queued one, if any. The context is the
// service's, not a socket's: closing the phone does not stop the review.
func (h *hub) runTurn(ctx context.Context, sessionID string, msg queuedMessage) {
	defer h.turnsWG.Done()
	if h.server.model != nil {
		outcome, err := h.server.model.HandleMessage(ctx, agent.TurnInput{
			SessionID: sessionID, Text: msg.text, ClientMsgID: msg.clientMsgID,
			ClientDate: msg.clientDate, Timezone: msg.tz,
		})
		switch {
		case err != nil:
			h.server.log.Warn("turn failed", "session", sessionID, "err", err.Error())
			h.publishError(sessionID, "internal", "Something went wrong. Your words are kept.", false)
		case outcome.Revises != "":
			h.setRevises(sessionID, outcome.Revises)
		}
	}

	h.mu.Lock()
	state := h.turns[sessionID]
	if state == nil {
		h.mu.Unlock()
		return
	}
	next := state.queued
	state.queued = nil
	cancel := state.cancel
	if next == nil {
		delete(h.turns, sessionID)
		cancel()
		h.mu.Unlock()
		return
	}
	// The queued message continues under a new context, so a stop of the finished turn does not cancel it.
	nextCtx, nextCancel := context.WithCancel(context.WithoutCancel(ctx))
	state.cancel = nextCancel
	cancel()
	h.turnsWG.Add(1)
	h.mu.Unlock()
	h.publishQueueState(sessionID, 0)
	go h.runTurn(nextCtx, sessionID, *next)
}

// stop cancels the turn in flight. It is explicit: nothing else ever stops a reply.
func (h *hub) stop(sessionID string) {
	h.mu.Lock()
	state := h.turns[sessionID]
	h.mu.Unlock()
	if state != nil {
		state.cancel()
	}
}

// finishTurn forgets a session's turn after a save or discard. A queued message is dropped too: the session it
// belonged to is closed, and running it would only be refused by the engine.
func (h *hub) finishTurn(sessionID string) {
	h.mu.Lock()
	state := h.turns[sessionID]
	delete(h.turns, sessionID)
	h.mu.Unlock()
	if state != nil {
		state.cancel()
	}
	h.publishQueueState(sessionID, 0)
}

func (h *hub) setRevises(sessionID, entryID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if state := h.turns[sessionID]; state != nil {
		state.revises = entryID
	}
}

func (h *hub) getRevises(sessionID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if state := h.turns[sessionID]; state != nil {
		return state.revises
	}
	return ""
}

func (h *hub) publishQueueState(sessionID string, queued int) {
	data, err := encodeEnvelope(nil, "queue_update", sessionID, h.server.now(), map[string]any{"queued": queued})
	if err != nil {
		return
	}
	h.publishSession(sessionID, data)
}

func (h *hub) publishError(sessionID, code, message string, retryable bool) {
	data, err := encodeEnvelope(nil, "error", sessionID, h.server.now(), map[string]any{
		"code": code, "message": message, "retryable": retryable,
	})
	if err != nil {
		return
	}
	h.publishSession(sessionID, data)
}

// rateLimiter is the per-session limit from docs/slices/S09-server.md: 10 messages a second and 60 a minute, and
// five refusals in ten seconds closes the socket. The limit is per session, not per connection, because two
// devices may share one session.
type rateLimiter struct {
	now func() time.Time

	mu        sync.Mutex
	buckets   map[string]*rateBucket
	lastSweep time.Time
}

type rateBucket struct {
	second   []time.Time
	minute   []time.Time
	refusals []time.Time
}

const (
	perSecond   = 10
	perMinute   = 60
	refusalCap  = 5
	refusalSpan = 10 * time.Second
)

func newRateLimiter(now func() time.Time) *rateLimiter {
	return &rateLimiter{now: now, buckets: map[string]*rateBucket{}}
}

// allow reports whether a message may be handled, and whether the socket must be closed for refusing too often.
func (r *rateLimiter) allow(sessionID string) (ok bool, closeIt bool) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Sub(r.lastSweep) > time.Minute {
		r.sweep(now)
		r.lastSweep = now
	}
	b := r.buckets[sessionID]
	if b == nil {
		b = &rateBucket{}
		r.buckets[sessionID] = b
	}
	b.second = within(b.second, now.Add(-time.Second))
	b.minute = within(b.minute, now.Add(-time.Minute))
	b.refusals = within(b.refusals, now.Add(-refusalSpan))
	if len(b.second) >= perSecond || len(b.minute) >= perMinute {
		b.refusals = append(b.refusals, now)
		return false, len(b.refusals) >= refusalCap
	}
	b.second = append(b.second, now)
	b.minute = append(b.minute, now)
	return true, false
}

// within drops the stamps older than the cut. It is a small slice, so a linear pass is the simplest correct one.
func within(stamps []time.Time, cut time.Time) []time.Time {
	kept := stamps[:0]
	for _, stamp := range stamps {
		if stamp.After(cut) {
			kept = append(kept, stamp)
		}
	}
	return kept
}

// sweep forgets a session that has been quiet for a minute, so the map cannot grow with every session ever opened.
func (r *rateLimiter) sweep(now time.Time) {
	for id, b := range r.buckets {
		if len(b.minute) == 0 || b.minute[len(b.minute)-1].Before(now.Add(-time.Minute)) {
			delete(r.buckets, id)
		}
	}
}

// Responder bridges the agent's question and the client's tap. The agent opens the interaction through the engine
// and then waits here; the socket resolves it through the engine (which writes the interaction_resolved event) and
// hands the value over. Nothing holds a database transaction while it waits.
type Responder struct {
	mu      sync.Mutex
	waiters map[string]chan json.RawMessage
	early   map[string]json.RawMessage
}

// NewResponder builds an empty bridge. The service makes one and hands it to both the agent and the hub.
func NewResponder() *Responder {
	return &Responder{waiters: map[string]chan json.RawMessage{}, early: map[string]json.RawMessage{}}
}

// Wait blocks until the question is answered or the turn ends.
func (r *Responder) Wait(ctx context.Context, sessionID, interactionID string) (json.RawMessage, error) {
	r.mu.Lock()
	if value, ok := r.early[interactionID]; ok {
		delete(r.early, interactionID)
		r.mu.Unlock()
		return value, nil
	}
	ch := make(chan json.RawMessage, 1)
	r.waiters[interactionID] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.waiters, interactionID)
		r.mu.Unlock()
	}()
	select {
	case value := <-ch:
		return value, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// deliver hands an answer to a waiting agent. If the answer arrived before the agent got to Wait (a very fast
// client), it is kept for a moment; the number of such answers is bounded by the open interactions.
func (r *Responder) deliver(interactionID string, value json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.waiters[interactionID]; ok {
		select {
		case ch <- value:
		default:
		}
		return
	}
	if len(r.early) >= 64 {
		for id := range r.early {
			delete(r.early, id)
			break
		}
	}
	r.early[interactionID] = value
}
