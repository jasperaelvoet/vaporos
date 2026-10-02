package extensions

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// The document lists the catalog in its order, each card filled from the
// shipped descriptor: verified permissions (the build's, never the
// descriptor's own words), downloads, settings with their values, actions,
// its web UI and status lines.
func TestDocumentCards(t *testing.T) {
	r := newRig(t)
	useHelper(t, "proton", &recHelper{status: []StatusLine{{Text: "Steam runs Windows games with it."}, {Text: ""}}})
	doc := r.doc()
	var ids []string
	for _, x := range doc.Extensions {
		ids = append(ids, x.ID)
	}
	if want := []string{"proton", "coolercontrol", "lact", "truckersmp", "star-citizen", "sc-hotas"}; !slices.Equal(ids, want) {
		t.Fatalf("cards %v, want %v", ids, want)
	}
	if doc.Restart.Needed || doc.Restart.Auto || doc.Restart.Reason != "" {
		t.Errorf("restart = %+v with nothing to do", doc.Restart)
	}

	p := r.card("proton")
	if p.State != StateInstalled || !p.Core || !p.Wanted || !p.Mounted || p.Size != 5000 || p.Category != "runtime" || p.Web != nil ||
		!slices.Equal(p.Permissions, []string{"modules", "compat-tool"}) || p.NeedsPassword || p.RunsAsRoot {
		t.Errorf("proton = %+v", p)
	}
	if !slices.Equal(p.Status, []StatusLine{{Text: "Steam runs Windows games with it."}}) {
		t.Errorf("proton status = %+v", p.Status)
	}

	c := r.card("coolercontrol")
	if c.State != StateNotInstalled || c.Wanted || c.Mounted || !c.RunsAsRoot || !c.NeedsPassword ||
		c.Web == nil || *c.Web != (WebDoc{Port: 11987, Label: "Open CoolerControl"}) ||
		c.Upstream.License != "GPL-3.0-or-later" || c.Copy.Remove != "Your fans go back to automatic." {
		t.Errorf("coolercontrol = %+v", c)
	}
	want := []SettingDoc{
		{Key: "overdrive", Type: "bool", Label: "Graphics card overclocking", Help: "Lets CoolerControl change the card's clocks.", Restart: true, Choices: []string{}, Value: false, NeedsPassword: true},
		{Key: "poll", Type: "choice", Label: "Sensor updates", Choices: []string{"normal", "slow"}, Value: "slow"},
	}
	if b, w := mustJSON(t, c.Settings), mustJSON(t, want); b != w {
		t.Errorf("coolercontrol settings = %s, want %s", b, w)
	}

	tm := r.card("truckersmp")
	if !slices.Equal(tm.Requires, []string{"proton"}) || len(tm.Downloads) != 1 || !tm.Downloads[0].RunsCode ||
		tm.Downloads[0].Checked != "publisher-hash" || len(tm.Actions) != 1 || tm.Actions[0].Confirm == nil ||
		tm.Actions[0].Confirm.Button != "Copy" || tm.NeedsPassword {
		t.Errorf("truckersmp = %+v", tm)
	}
	if pr := r.card("proton"); !slices.Equal(pr.RequiredBy, []string{}) {
		t.Errorf("proton is required by %v with nothing added", pr.RequiredBy)
	}

	// Every list is a list, never null: the page iterates them.
	b, _ := json.Marshal(r.card("lact"))
	for _, k := range []string{"caveats", "downloads", "settings", "actions", "status", "requires", "required_by"} {
		if !strings.Contains(string(b), `"`+k+`":[]`) {
			t.Errorf("lact %s is not an empty list: %s", k, b)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	must(t, err)
	return string(b)
}

// Adding an extension walks its card through installing (queued, then
// downloading), restart-needed and, after the restart, installed with its
// web link; removing it is restart-needed until the restart.
func TestDocumentStatesThroughAnInstall(t *testing.T) {
	r := newRig(t)
	if code, body := r.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`); code != 200 {
		t.Fatalf("install: %d %s", code, body)
	}
	if c := r.card("coolercontrol"); c.State != StateInstalling || !c.Wanted || c.Progress != nil {
		t.Fatalf("queued = %+v", c)
	}

	// While its bytes arrive, the card shows them.
	r.s.fetchStart(r.imgs["coolercontrol"].entry, true)
	r.s.fetchProgress(r.imgs["coolercontrol"].entry, true, 1000)
	if c := r.card("coolercontrol"); c.State != StateInstalling || c.Progress == nil || *c.Progress != (Progress{Bytes: 1000, Total: 2000}) {
		t.Fatalf("downloading = %+v", c)
	}
	r.s.fetchEnd(r.imgs["coolercontrol"].entry, true)

	r.pass()
	doc := r.doc()
	if c := r.card("coolercontrol"); c.State != StateRestartNeeded {
		t.Fatalf("after the pass = %+v", c)
	}
	if !doc.Restart.Needed || doc.Restart.Reason != "Restart to finish adding CoolerControl." {
		t.Fatalf("restart = %+v", doc.Restart)
	}
	if !r.s.RestartNeeded() {
		t.Fatal("RestartNeeded is false with a set pending")
	}

	r.boot()
	c := r.card("coolercontrol")
	if c.State != StateInstalled || !c.Mounted || c.Web == nil || *c.Web != (WebDoc{Port: 11987, Label: "Open CoolerControl"}) {
		t.Fatalf("after the restart = %+v", c)
	}
	if d := r.doc(); d.Restart.Needed {
		t.Fatalf("restart still needed: %+v", d.Restart)
	}

	if code, body := r.do("DELETE", "/extensions/coolercontrol", ""); code != 200 {
		t.Fatalf("remove: %d %s", code, body)
	}
	if c := r.card("coolercontrol"); c.State != StateRestartNeeded || c.Wanted || !c.Mounted {
		t.Fatalf("removed = %+v", c)
	}
	r.pass()
	if d := r.doc(); !d.Restart.Needed || d.Restart.Reason != "Restart to finish removing CoolerControl." {
		t.Fatalf("restart after removing = %+v", d.Restart)
	}
}

// Core is never "not installed": until its image is there it is
// installing, and on a start without extensions it needs attention.
func TestDocumentCoreIsNeverNotInstalled(t *testing.T) {
	r := newRig(t)
	r.report(store.BootReport{Mode: store.ModeOff, Reason: store.ReasonSkipOnce})
	r.s, r.b = r.service()
	r.wire()
	r.pass()
	if p := r.card("proton"); p.State != StateNeedsAttention || p.Reason != startedOffText || p.Mounted {
		t.Fatalf("proton on a start without extensions = %+v", p)
	}

	// A new install whose image is still to come, as Run lists it before
	// its first pass.
	r2 := newRig(t)
	must(t, os.Remove(store.ImagePath(r2.imgs["proton"].entry.SHA256)))
	r2.report(store.BootReport{Mode: store.ModeEnabled, Reason: store.ReasonNoSet})
	r2.s, r2.b = r2.service()
	r2.wire()
	r2.s.view.cat, r2.s.view.version = r2.b.cat, r2.b.version
	if p := r2.card("proton"); p.State != StateInstalling || p.Mounted {
		t.Fatalf("proton before the first pass = %+v", p)
	}
}

// A set that failed its trial, a helper that could not set its extension
// up and a status line of tone error each need attention, with the words
// why.
func TestDocumentNeedsAttention(t *testing.T) {
	r := newRig(t)
	cat := r.b.cat
	r.seal(r.imgs["truckersmp"])
	locked(t, func() error {
		if err := store.WriteWanted([]string{"truckersmp"}); err != nil {
			return err
		}
		return store.AddFailed(store.Fingerprint(store.Pairs(cat, []string{"proton", "truckersmp"}), nil))
	})
	r.pass()
	if x := r.card("truckersmp"); x.State != StateNeedsAttention || x.Reason != blockedText {
		t.Fatalf("blocked = %+v", x)
	}

	h := &recHelper{status: []StatusLine{{Text: "You deleted its Steam shortcut. Remove it and add it again to get it back.", Tone: "error"}}}
	useHelper(t, "proton", h)
	if x := r.card("proton"); x.State != StateNeedsAttention || !strings.Contains(x.Reason, "deleted its Steam shortcut") {
		t.Fatalf("status line of tone error = %+v", x)
	}
}

// The publisher sends the document when it changes, no more often than
// publishGap, and not again while it stays the same.
func TestPublishLoop(t *testing.T) {
	r := newRig(t)
	gap, poll := publishGap, publishPoll
	t.Cleanup(func() { publishGap, publishPoll = gap, poll })
	publishGap, publishPoll = 200*time.Millisecond, time.Hour
	ctx := t.Context()
	go r.s.publishLoop(ctx)
	count := func() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.events) }

	r.s.changed()
	waitFor(t, func() bool { return count() == 1 })
	start := time.Now()
	locked(t, func() error { return store.WriteWanted([]string{"truckersmp"}) })
	r.s.changed()
	r.s.changed()
	waitFor(t, func() bool { return count() == 2 })
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Errorf("second event after %v, within the gap", d)
	}
	r.s.changed() // nothing new
	time.Sleep(400 * time.Millisecond)
	if n := count(); n != 2 {
		t.Fatalf("%d events, want 2 (the same document is not sent twice)", n)
	}
	r.mu.Lock()
	last := r.events[1]
	r.mu.Unlock()
	for _, x := range last.Extensions {
		if x.ID == "truckersmp" && (x.State != StateInstalling || !x.Wanted) {
			t.Errorf("event's truckersmp = %+v", x)
		}
	}
}
