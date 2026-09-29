package display

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/welcome"
	"github.com/jasperaelvoet/vaporos/internal/session"
)

// TestRunLoop drives the real policy loop: session socket, welcome.json,
// event overlays and hotplug, with the fake host.
func TestRunLoop(t *testing.T) {
	m, h, _, hub := newTestManager(t, true)
	m.now = time.Now
	m.scanEvery, m.refreshEvery, m.verifyEvery = 5*time.Millisecond, 5*time.Millisecond, 20*time.Millisecond
	// Unix socket paths are limited to ~104 bytes on macOS.
	short, err := os.MkdirTemp("", "vr")
	if err != nil {
		t.Fatal(err)
	}
	saveRun := config.RunDir
	config.RunDir = short
	t.Cleanup(func() { config.RunDir = saveRun; os.RemoveAll(short) })
	hot := make(chan struct{}, 1)
	h.hotplug = hot

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	readWelcome := func() welcome.State {
		var st welcome.State
		config.ReadJSON(config.WelcomeStatePath(), &st)
		return st
	}
	waitFor(t, func() bool { return h.isActive(WelcomeUnit, false) })
	waitFor(t, func() bool { return readWelcome().Status == "Ready to stream" })
	waitFor(t, func() bool {
		_, err := os.Stat(config.SessionSock())
		return err == nil
	})

	resp, err := session.Call(ctx, config.SessionSock(), session.Request{Op: "begin", Client: "Deck", Width: 1280, Height: 800, FPS: 90})
	if err != nil || !resp.OK || resp.Mode != "1280x800@90" {
		t.Fatalf("begin over the socket = %+v, %v", resp, err)
	}
	if !h.isActive(GamescopeUnit, true) || h.isActive(WelcomeUnit, false) {
		t.Fatal("session did not swap welcome for gamescope")
	}
	waitFor(t, func() bool { return readWelcome().Status == "Streaming to Deck" })

	hub.Publish("pairing.pending", map[string]string{"name": "Phone"})
	waitFor(t, func() bool { return readWelcome().Status == "Phone wants to pair" })

	if resp, err := session.Call(ctx, config.SessionSock(), session.Request{Op: "end"}); err != nil || !resp.OK {
		t.Fatalf("end = %+v, %v", resp, err)
	}
	// Within the grace period, the monitor going away keeps gamescope (no
	// monitor means gamescope anyway), and verify keeps the welcome off.
	h.setMonitor(false)
	hot <- struct{}{}
	time.Sleep(50 * time.Millisecond)
	if !h.isActive(GamescopeUnit, true) || h.isActive(WelcomeUnit, false) {
		t.Fatal("wrong units after unplug")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
	// Stopping vosd leaves the display alone.
	if !h.isActive(GamescopeUnit, true) {
		t.Error("Run tore gamescope down on exit")
	}
}
