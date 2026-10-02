package extensions

import (
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

// A disk setting holds a drive the control center offers: the system
// drive or a game drive's folder; "" (none picked) only when the
// extension can do without one.
func TestCheckDiskSetting(t *testing.T) {
	required := descriptor.Setting{Key: "disk", Type: "disk", Required: true}
	optional := descriptor.Setting{Key: "disk", Type: "disk"}
	for _, v := range []any{"/var", "/var/mnt/Games", "/var/mnt/SATA_1TB-2", "/var/mnt/..x"} {
		for _, s := range []descriptor.Setting{required, optional} {
			if got, err := CheckSetting(s, v); err != nil || got != v {
				t.Errorf("%v (required %v) = %v, %v", v, s.Required, got, err)
			}
		}
	}
	for _, v := range []any{"/state", "/var/mnt", "/var/mnt/", "/var/mnt/a/b", "/var/mnt/..", "/var/mnt/.", "/mnt/Games",
		"/var/mnt/a b", "/home/vapor", "var", 5, nil, "/var/mnt/" + strings.Repeat("a", 256)} {
		if _, err := CheckSetting(optional, v); err == nil {
			t.Errorf("%v accepted", v)
		}
	}
	if _, err := CheckSetting(required, ""); err == nil || !strings.Contains(err.Error(), "disk must be the system drive (/var) or a game drive") {
		t.Errorf("no drive for a required setting: %v", err)
	}
	if v, err := CheckSetting(optional, ""); err != nil || v != "" {
		t.Errorf("no drive for an optional setting: %v %v", v, err)
	}
}

// A drive the person picked before is read as it was meant: /state was
// the system drive's folder; a value no longer taken reads as none.
func TestStoredDrives(t *testing.T) {
	r := newRig(t)
	d := r.s.desc("star-citizen")
	for stored, want := range map[string]string{
		`{"disk":"/state"}`:          SystemDrive,
		`{"disk":"/var/mnt/Games"}`:  "/var/mnt/Games",
		`{"disk":"/srv/games"}`:      "",
		`{"disk":""}`:                "",
		`{"other":"/var/mnt/Games"}`: "",
	} {
		writeFile(t, settingsPath("star-citizen"), stored)
		if got := loadSettings("star-citizen", d)["disk"]; got != want {
			t.Errorf("%s reads as %q, want %q", stored, got, want)
		}
	}
}

// An extension that needs a drive is never set up without one: its card
// asks for one, nothing tries again by itself, and "Try again" sets it up
// once a drive is picked. Its card says the setting is required.
func TestSetupWaitsForADrive(t *testing.T) {
	r := newRig(t)
	h := &recHelper{}
	useHelper(t, "star-citizen", h)
	if code, body := r.do("POST", "/extensions/star-citizen", `{"options":{"disk":"/var/mnt/Games"}}`); code != 200 {
		t.Fatalf("install: %d %s", code, body)
	}
	if s := r.card("star-citizen").Settings[0]; !s.Required || s.Value != "/var/mnt/Games" {
		t.Fatalf("setting = %+v", s)
	}
	r.pass()
	writeFile(t, settingsPath("star-citizen"), `{"disk":""}`) // as an earlier VaporOS left it: the system drive then
	r.boot()
	x := r.card("star-citizen")
	if x.State != StateNeedsAttention || x.Reason != "Pick a game drive for Star Citizen, then select Try again." {
		t.Fatalf("without a drive = %+v", x)
	}
	r.pass()
	r.pass()
	if len(h.Calls()) != 0 || isInstalled("star-citizen") {
		t.Fatalf("set up without a drive: %q", h.Calls())
	}
	if code, body := r.do("PUT", "/extensions/star-citizen/settings", `{"settings":{"disk":""}}`); code != 400 {
		t.Fatalf("no drive for a required setting: %d %s", code, body)
	}
	if code, body := r.do("PUT", "/extensions/star-citizen/settings", `{"settings":{"disk":"/var"}}`); code != 200 {
		t.Fatalf("picking the system drive: %d %s", code, body)
	}
	if code, body := r.do("POST", "/extensions/star-citizen/retry", ""); code != 200 {
		t.Fatalf("retry: %d %s", code, body)
	}
	r.pass()
	if x := r.card("star-citizen"); x.State != StateInstalled || x.Reason != "" ||
		!slices.Equal(h.Calls(), []string{"install star-citizen"}) {
		t.Fatalf("after Try again = %+v, calls %q", x, h.Calls())
	}
}
