// Package events is the in-process pub/sub hub behind the SSE stream
// (GET /api/v1/events) and the welcome screen. Topics are listed in
// docs/CONTRACTS.md.
package events

import (
	"encoding/json"
	"sync"
)

type Event struct {
	Topic string          `json:"topic"`
	Data  json.RawMessage `json:"data"`
}

type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
	last map[string]Event
}

func NewHub() *Hub { return &Hub{subs: map[chan Event]struct{}{}, last: map[string]Event{}} }

// Default is the process-wide hub.
var Default = NewHub()

// transient topics are one-off notifications, not state. Live subscribers
// get them, but Last does not replay them: every page load opens a new
// stream, and a replayed "X wants to pair" or "power-off failed" would show
// again long after it stopped being true. Pages read the matching state
// (pending pairings, for one) over REST.
var transient = map[string]bool{"system.message": true, "pairing.pending": true}

// Publish sends data (marshalled to JSON) to every subscriber. Slow
// subscribers drop events rather than block the publisher.
func (h *Hub) Publish(topic string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	ev := Event{Topic: topic, Data: b}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !transient[topic] {
		h.last[topic] = ev
	}
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Subscribe returns a channel of events and a cancel func.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// Last returns the most recent event of each state topic, for late
// subscribers. Transient notification topics are never replayed.
func (h *Hub) Last() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Event, 0, len(h.last))
	for _, ev := range h.last {
		out = append(out, ev)
	}
	return out
}

func Publish(topic string, data any) { Default.Publish(topic, data) }
