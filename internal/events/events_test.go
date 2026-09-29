package events

import (
	"slices"
	"sort"
	"testing"
)

func TestLastReplaysStateNotNotifications(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe()
	defer cancel()
	h.Publish("display.changed", struct{}{})
	h.Publish("system.message", map[string]string{"level": "error", "text": "Automatic power-off failed"})
	h.Publish("pairing.pending", map[string]string{"name": "TV"})
	h.Publish("update.state", map[string]string{"booted": "1"})
	h.Publish("update.state", map[string]string{"booted": "2"})

	// Live subscribers get everything, notifications included.
	var live []string
	for range 5 {
		live = append(live, (<-ch).Topic)
	}
	want := []string{"display.changed", "system.message", "pairing.pending", "update.state", "update.state"}
	if !slices.Equal(live, want) {
		t.Fatalf("live events = %v, want %v", live, want)
	}

	// A page that connects later sees only state, the latest per topic.
	last := h.Last()
	var got []string
	for _, ev := range last {
		got = append(got, ev.Topic)
		if ev.Topic == "update.state" && string(ev.Data) != `{"booted":"2"}` {
			t.Errorf("update.state replay = %s, want the latest", ev.Data)
		}
	}
	sort.Strings(got)
	if !slices.Equal(got, []string{"display.changed", "update.state"}) {
		t.Fatalf("Last() topics = %v, want [display.changed update.state]", got)
	}
}

func TestPublishNeverBlocksOnSlowSubscribers(t *testing.T) {
	h := NewHub()
	_, cancel := h.Subscribe()
	defer cancel()
	for range 200 { // more than the channel buffers
		h.Publish("update.progress", map[string]int{"percent": 1})
	}
}
