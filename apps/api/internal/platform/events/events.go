package events

import (
	"context"
	"sync"
)

// Event is a domain event emitted by the business modules (order.created,
// order.paid, product.updated, inventory.low, ...). This in-process pub/sub is
// the seam a future webhooks module subscribes to without touching the modules
// that produce the events. See docs/04-agent-build-spec.md "Forward
// compatibility".
type Event struct {
	Name string
	Data any
}

// Handler receives a single event.
type Handler func(ctx context.Context, e Event) error

// Bus is a minimal in-process pub/sub.
type Bus struct {
	mu       sync.RWMutex
	handlers map[string][]Handler
}

// NewBus returns an empty event bus.
func NewBus() *Bus {
	return &Bus{handlers: make(map[string][]Handler)}
}

// Subscribe registers a handler for an event name.
func (b *Bus) Subscribe(name string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[name] = append(b.handlers[name], h)
}

// Emit synchronously delivers an event to all its handlers. A handler error is
// returned up so the caller can decide whether to fail the surrounding unit of
// work.
func (b *Bus) Emit(ctx context.Context, e Event) error {
	b.mu.RLock()
	hs := append([]Handler(nil), b.handlers[e.Name]...)
	b.mu.RUnlock()

	for _, h := range hs {
		if err := h(ctx, e); err != nil {
			return err
		}
	}
	return nil
}
