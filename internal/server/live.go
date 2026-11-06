package server

import (
	"sync"

	"github.com/AutumnsGrove/sepia/internal/agent"
)

// LiveBus carries the agent's live-only events to the sockets. The agent is built before the server (the server
// needs it), so the agent gets Publish as its callback and the server attaches the hub's fan-out when it is made.
// It is the one seam that lets a turn publish without knowing whether a socket exists.
type LiveBus struct {
	mu   sync.RWMutex
	sink func(agent.Event)
}

// NewLiveBus builds an unattached bus.
func NewLiveBus() *LiveBus { return &LiveBus{} }

// Publish is the agent's live callback. Before the server attaches, it does nothing: a turn run without a socket
// still writes every persisted event.
func (b *LiveBus) Publish(ev agent.Event) {
	b.mu.RLock()
	sink := b.sink
	b.mu.RUnlock()
	if sink != nil {
		sink(ev)
	}
}

// attach points the bus at the hub. It is called once, when the server is built.
func (b *LiveBus) attach(sink func(agent.Event)) {
	b.mu.Lock()
	b.sink = sink
	b.mu.Unlock()
}
