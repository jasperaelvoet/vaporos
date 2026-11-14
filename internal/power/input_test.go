package power

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// testWatch is an InputWatch over FIFOs standing in for event devices.
func testWatch(t *testing.T) (*InputWatch, *time.Time) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	w := &InputWatch{dev: t.TempDir(), sys: t.TempDir(), now: func() time.Time { return now }}
	return w, &now
}

// addDevice makes an event device and returns what writes input to it.
func addDevice(t *testing.T, w *InputWatch, name string) *os.File {
	t.Helper()
	p := filepath.Join(w.dev, name)
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestAttendedStreamNeedsInput(t *testing.T) {
	w, now := testWatch(t)
	dev := addDevice(t, w, "event3")
	on := true
	stream := w.Attended(func() (bool, string) { return on, "Moonlight stream" })

	if busy, why := stream(); !busy || why != "Moonlight stream" {
		t.Fatalf("a stream that just started = %v %q, want busy", busy, why)
	}
	*now = now.Add(attendedWindow - time.Second)
	if busy, _ := stream(); !busy {
		t.Fatal("idle before the window passed")
	}
	*now = now.Add(time.Second)
	if busy, why := stream(); busy || why != "" {
		t.Fatalf("an untouched stream = %v %q, want idle", busy, why)
	}
	*now = now.Add(time.Hour)
	if busy, _ := stream(); busy {
		t.Fatal("an untouched stream became busy again")
	}

	if _, err := dev.Write(make([]byte, 24)); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(15 * time.Second)
	if busy, _ := stream(); !busy {
		t.Fatal("input did not make the stream busy")
	}
	*now = now.Add(attendedWindow)
	if busy, _ := stream(); busy {
		t.Fatal("old input still counts")
	}

	// No stream: input alone keeps nothing awake.
	on = false
	dev.Write(make([]byte, 24))
	*now = now.Add(15 * time.Second)
	if busy, _ := stream(); busy {
		t.Fatal("busy without a stream")
	}
}

func TestAttendedCountsStreamChanges(t *testing.T) {
	w, now := testWatch(t)
	on, why := true, "Moonlight stream"
	stream := w.Attended(func() (bool, string) { return on, why })
	stream()
	*now = now.Add(time.Hour)
	if busy, _ := stream(); busy {
		t.Fatal("want idle")
	}
	// A client that left, and one that came back.
	why = "Moonlight stream (waiting for the client to reconnect)"
	if busy, got := stream(); !busy || got != why {
		t.Fatalf("a changed reason = %v %q, want busy", busy, got)
	}
	*now = now.Add(time.Hour)
	on = false
	stream()
	on = true
	if busy, _ := stream(); !busy {
		t.Fatal("a new stream is not busy")
	}
}

func TestInputWatchDevices(t *testing.T) {
	w, now := testWatch(t)
	stream := w.Attended(func() (bool, string) { return true, "s" })
	stream()
	*now = now.Add(time.Hour)

	// A controller that appears counts; its motion sensors never do.
	motion := filepath.Join(w.sys, "event8", "device")
	if err := os.MkdirAll(motion, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(motion, "properties"), []byte("40\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sensors := addDevice(t, w, "event8")
	sensors.Write(make([]byte, 48))
	addDevice(t, w, "mouse0").Write(make([]byte, 8)) // not an event device
	if busy, _ := stream(); busy {
		t.Fatal("motion sensors count as input")
	}
	pad := addDevice(t, w, "event7")
	*now = now.Add(2 * time.Second)
	if busy, _ := stream(); !busy {
		t.Fatal("a new device does not count as input")
	}

	// A device that went is forgotten, and one of its name is read again.
	*now = now.Add(time.Hour)
	stream()
	pad.Close()
	if err := os.Remove(filepath.Join(w.dev, "event7")); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Second)
	if busy, _ := stream(); busy {
		t.Fatal("a removed device counts as input")
	}
	if _, ok := w.fds["event7"]; ok {
		t.Fatal("a removed device stays open")
	}
	*now = now.Add(time.Hour)
	again := addDevice(t, w, "event7")
	stream()
	*now = now.Add(time.Hour)
	again.Write(make([]byte, 24))
	if busy, _ := stream(); !busy {
		t.Fatal("input on a device that came back does not count")
	}
}
