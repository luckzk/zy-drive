package app

import "sync"

// EventType classifies progress events pushed to the UI.
type EventType string

const (
	EventBrowseRefreshed  EventType = "browse.refreshed"
	EventBrowseError      EventType = "browse.error"
	EventTransferProgress EventType = "transfer.progress"
	EventJobDone          EventType = "job.done"
	EventJobFailed        EventType = "job.failed"
)

// Event is one UI-facing progress notification. It carries no secrets.
type Event struct {
	Type    EventType `json:"type"`
	MountID string    `json:"mountId,omitempty"`
	JobID   string    `json:"jobId,omitempty"`
	Message string    `json:"message,omitempty"`
}

// EventBus is a tiny in-process pub/sub for UI progress. The frontend
// subscribes via mygo events (desktop) or SSE (Agent); the bus itself
// knows nothing about transports.
type EventBus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

// NewEventBus returns an empty bus.
func NewEventBus() *EventBus { return &EventBus{subs: map[chan Event]struct{}{}} }

// Subscribe returns a channel receiving events. Callers must call
// Unsubscribe when done.
func (b *EventBus) Subscribe() chan Event {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscription.
func (b *EventBus) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// Publish sends e to all subscribers, dropping it for slow subscribers
// rather than blocking the engine.
func (b *EventBus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
