package api

import (
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/events"
)

// sseWriteTimeout bounds one write to a stalled client, so a phone that
// vanished from Wi-Fi does not pin a goroutine until TCP gives up.
const sseWriteTimeout = 30 * time.Second

// handleEvents is GET /api/v1/events: every hub event as Server-Sent Events,
// starting with the latest event of each topic so a fresh page has state.
// A comment line every heartbeat keeps proxies and NAT from idling the
// connection out and notices when the session has ended.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// Subscribe before reading Last so nothing published in between is
	// lost; at worst a topic arrives twice, and events are snapshots.
	ch, cancel := s.hub.Subscribe()
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(write func(io.Writer) error) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
		if err := write(w); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	initial := s.hub.Last()
	sort.Slice(initial, func(i, j int) bool { return initial[i].Topic < initial[j].Topic })
	ok := send(func(w io.Writer) error {
		if _, err := io.WriteString(w, "retry: 3000\n\n"); err != nil {
			return err
		}
		for _, ev := range initial {
			if err := writeEvent(w, ev); err != nil {
				return err
			}
		}
		return nil
	})
	if !ok {
		return
	}

	tick := time.NewTicker(s.heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open || !send(func(w io.Writer) error { return writeEvent(w, ev) }) {
				return
			}
		case <-tick.C:
			if !s.stillAllowed(r) {
				return
			}
			if !send(func(w io.Writer) error { _, err := io.WriteString(w, ": ping\n\n"); return err }) {
				return
			}
		}
	}
}

// writeEvent formats one event. Data is JSON and normally one line; any
// newline is split across data: lines as the SSE format requires.
func writeEvent(w io.Writer, ev events.Event) error {
	var b strings.Builder
	topic := strings.NewReplacer("\r", "", "\n", "").Replace(ev.Topic)
	b.WriteString("event: ")
	b.WriteString(topic)
	b.WriteByte('\n')
	data := string(ev.Data)
	if data == "" {
		data = "null"
	}
	for _, line := range strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n") {
		b.WriteString("data: ")
		b.WriteString(strings.TrimSuffix(line, "\r"))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}
