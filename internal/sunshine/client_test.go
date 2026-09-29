package sunshine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestClientTalksToSunshine(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cl := h.s.api()
	h.f.clients = []PairedClient{{UUID: "A1", Name: "MacBook", Enabled: true}}
	h.f.pairings = []Pairing{{ID: strings.Repeat("ab", 16), Name: "iPhone", Address: "192.168.1.20"}}

	clients, err := cl.Clients(ctx)
	if err != nil || !reflect.DeepEqual(clients, h.f.clients) {
		t.Fatalf("Clients = %+v, %v", clients, err)
	}
	ps, err := cl.PendingPairings(ctx)
	if err != nil || !reflect.DeepEqual(ps, h.f.pairings) {
		t.Fatalf("PendingPairings = %+v, %v", ps, err)
	}
	if ok, err := cl.Pair(ctx, ps[0].ID, "1234", "iPhone"); err != nil || !ok {
		t.Errorf("Pair = %v, %v", ok, err)
	}
	if ok, err := cl.Pair(ctx, ps[0].ID, "9999", "iPhone"); err != nil || ok {
		t.Errorf("Pair with a wrong PIN = %v, %v", ok, err)
	}
	if v, err := cl.Version(ctx); err != nil || v != "2026.928.101500" {
		t.Errorf("Version = %q, %v", v, err)
	}
	if ok, err := cl.Unpair(ctx, "A1"); err != nil || !ok {
		t.Errorf("Unpair = %v, %v", ok, err)
	}
	if ok, err := cl.Unpair(ctx, "A1"); err != nil || ok {
		t.Errorf("second Unpair = %v, %v", ok, err)
	}
	if logs, err := cl.Logs(ctx); err != nil || logs != "line 1\nline 2\n" {
		t.Errorf("Logs = %q, %v", logs, err)
	}
	if err := cl.CloseApp(ctx); err != nil || h.f.closed != 1 {
		t.Errorf("CloseApp = %v (closed %d)", err, h.f.closed)
	}
	if err := cl.Restart(ctx); err != nil {
		t.Errorf("Restart = %v", err)
	}
	// Sunshine skips CSRF checks only for requests without Origin/Referer.
	if len(h.f.badHeader) != 0 {
		t.Errorf("sent browser headers: %v", h.f.badHeader)
	}
	// JSON bodies carried their content type (the fake rejects them otherwise).
	if len(h.f.bodies) < 4 || h.f.bodies[0]["pairing_id"] != ps[0].ID || h.f.bodies[0]["name"] != "iPhone" {
		t.Errorf("bodies = %v", h.f.bodies)
	}
}

func TestClientPinsSunshineCertificate(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cl := h.s.api()
	writePEM(t, certPath(), otherCert(t))
	if _, err := cl.Clients(ctx); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a different certificate was accepted: %v", err)
	}
	os.WriteFile(certPath(), []byte("not a pem"), 0o600)
	if _, err := cl.Clients(ctx); err == nil {
		t.Fatal("an unreadable pin file was treated as no pin")
	}
	// Before Sunshine first starts there is nothing to pin yet.
	os.Remove(certPath())
	cl2 := NewClient(h.api.URL, h.f.user, h.f.pass, certPath())
	if _, err := cl2.Clients(ctx); err != nil {
		t.Errorf("without a pin file: %v", err)
	}
}

func TestClientErrorMapping(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	wrong := NewClient(h.api.URL, "vosd", "wrong", certPath())
	if _, err := wrong.Clients(ctx); !errors.Is(err, errUnauthorized) {
		t.Errorf("wrong password: %v", err)
	}
	h.f.noCreds = true
	if _, err := h.s.api().Clients(ctx); !errors.Is(err, errUnauthorized) {
		t.Errorf("redirect to /welcome: %v", err)
	}
	h.f.noCreds, h.f.oldAPI = false, true
	if _, err := h.s.api().PendingPairings(ctx); !errors.Is(err, errNotSupported) {
		t.Errorf("old Sunshine: %v", err)
	}
	// Sunshine's own 400 message is passed on.
	if _, err := h.s.api().Pair(ctx, "", "1234", "x"); err != nil {
		t.Errorf("old-API pairing: %v", err)
	}
	h.f.oldAPI = false
	if _, err := h.s.api().Pair(ctx, "", "1234", "x"); err == nil || !strings.Contains(err.Error(), "32 hexadecimal") {
		t.Errorf("400 message = %v", err)
	}
	dead := NewClient("https://127.0.0.1:1", "u", "p", certPath())
	if _, err := dead.Version(ctx); err == nil {
		t.Error("a closed port answered")
	}
}

func TestParseServerInfo(t *testing.T) {
	for state, want := range map[string]bool{"SUNSHINE_SERVER_BUSY": true, "SUNSHINE_SERVER_FREE": false} {
		busy, err := parseServerInfo([]byte(fmt.Sprintf(serverInfoXML, state)))
		if err != nil || busy != want {
			t.Errorf("%s: busy=%v err=%v", state, busy, err)
		}
	}
	if _, err := parseServerInfo([]byte("<html>")); err == nil {
		t.Error("garbage parsed")
	}
	h := newHarness(t)
	if b, _ := h.s.Busy(); b {
		t.Error("idle Sunshine reported busy")
	}
	h.f.setBusy(true)
	if b, why := h.s.Busy(); !b || why != "Moonlight stream" {
		t.Errorf("Busy = %v, %q", b, why)
	}
	h.s.infoURL = "http://127.0.0.1:1/serverinfo"
	if b, _ := h.s.Busy(); b {
		t.Error("unreachable Sunshine reported busy")
	}
}
