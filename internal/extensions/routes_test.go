package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/auth"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

func wantedNow(t *testing.T) []string {
	t.Helper()
	w, err := store.Wanted()
	must(t, err)
	return w
}

// What POST /extensions/{id} refuses, before it changes anything.
func TestInstallRefuses(t *testing.T) {
	r := newRig(t)
	locked(t, func() error { return store.WriteWanted([]string{"lact", "gone"}) })
	r.pass()
	for _, c := range []struct {
		path, body string
		code       int
		msg        string
	}{
		{"/extensions/nope", `{}`, 404, `no extension \"nope\"`},
		{"/extensions/Not_An_ID", `{}`, 404, `no extension \"Not_An_ID\"`},
		{"/extensions/nope", `{`, 400, "bad request body"},
		{"/extensions/gone", `{}`, 409, "This version of VaporOS does not have gone."},
		{"/extensions/proton", `{}`, 409, "CachyOS Proton is part of VaporOS and always on."},
		{"/extensions/coolercontrol", `{"password":"` + rigPassword + `"}`, 409, "CoolerControl cannot run together with LACT."},
		{"/extensions/star-citizen", `{"options":{"disk":"relative/path"}}`, 400, "disk must be the system drive (/var) or a game drive (a folder in /var/mnt)"},
		{"/extensions/star-citizen", `{"options":{"disk":"/state"}}`, 400, "disk must be the system drive (/var) or a game drive (a folder in /var/mnt)"},
		{"/extensions/star-citizen", `{"options":{"disk":""}}`, 400, "disk must be the system drive"},
		{"/extensions/star-citizen", `{}`, 400, "Star Citizen needs a game drive. Pick one to add it."},
		{"/extensions/sc-hotas", `{}`, 400, "HOTAS for Star Citizen needs Star Citizen, which needs a game drive. Add Star Citizen first."},
		{"/extensions/star-citizen", `{"options":{"color":"red"}}`, 400, `unknown setting \"color\"`},
		{"/extensions/star-citizen", `{"options":[]}`, 400, "bad request body"},
	} {
		code, body := r.do("POST", c.path, c.body)
		if code != c.code || !strings.Contains(body, c.msg) {
			t.Errorf("POST %s %s = %d %s, want %d %q", c.path, c.body, code, body, c.code, c.msg)
		}
	}
	if w := wantedNow(t); !slices.Equal(w, []string{"gone", "lact"}) {
		t.Errorf("wanted = %v after refusals", w)
	}
}

// An extension that runs as root, or sets kernel module options, takes the
// admin password; one that does neither does not.
func TestInstallPassword(t *testing.T) {
	r := newRig(t)
	if code, body := r.do("POST", "/extensions/coolercontrol", `{}`); code != 403 || !strings.Contains(body, "Adding CoolerControl needs the admin password") {
		t.Fatalf("no password: %d %s", code, body)
	}
	if code, body := r.do("POST", "/extensions/coolercontrol", `{"password":"guess"}`); code != 403 || !strings.Contains(body, "the password is wrong") {
		t.Fatalf("wrong password: %d %s", code, body)
	}
	if len(wantedNow(t)) != 0 {
		t.Fatal("a refused install changed wanted")
	}
	code, body := r.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`","options":{"overdrive":true}}`)
	if code != 200 {
		t.Fatalf("install: %d %s", code, body)
	}
	var doc Document
	must(t, json.Unmarshal([]byte(body), &doc))
	if i := slices.IndexFunc(doc.Extensions, func(x ExtensionDoc) bool { return x.ID == "coolercontrol" }); i < 0 ||
		doc.Extensions[i].State != StateInstalling || doc.Extensions[i].Settings[0].Value != true {
		t.Fatalf("answer = %s", body)
	}
	if w := wantedNow(t); !slices.Equal(w, []string{"coolercontrol"}) {
		t.Fatalf("wanted = %v", w)
	}
	// The first settings are stored whole: the options given and the defaults.
	b, err := os.ReadFile(settingsPath("coolercontrol"))
	must(t, err)
	var stored map[string]any
	must(t, json.Unmarshal(b, &stored))
	if stored["overdrive"] != true || stored["poll"] != "slow" {
		t.Fatalf("settings file = %s", b)
	}

	before := r.reauths
	if code, _ := r.do("POST", "/extensions/truckersmp", `{}`); code != 200 || r.reauths != before {
		t.Fatalf("truckersmp: %d, %d password checks", code, r.reauths-before)
	}
}

// Adding an extension adds what it requires; removing one that another
// needs is refused, as is removing core.
func TestInstallRequiresAndRemoveRefuses(t *testing.T) {
	r := newRig(t)
	if code, body := r.do("POST", "/extensions/star-citizen", `{"options":{"disk":"/var/mnt/Games"}}`); code != 200 {
		t.Fatalf("install star-citizen: %d %s", code, body)
	}
	if code, body := r.do("POST", "/extensions/sc-hotas", `{}`); code != 200 {
		t.Fatalf("install: %d %s", code, body)
	}
	if w := wantedNow(t); !slices.Equal(w, []string{"sc-hotas", "star-citizen"}) {
		t.Fatalf("wanted = %v, want sc-hotas with star-citizen (proton is core)", w)
	}
	if x := r.card("star-citizen"); !slices.Equal(x.RequiredBy, []string{"sc-hotas"}) || x.State != StateInstalling {
		t.Fatalf("star-citizen = %+v", x)
	}
	for _, c := range []struct {
		path string
		code int
		msg  string
	}{
		{"/extensions/star-citizen", 409, "HOTAS for Star Citizen needs Star Citizen. Remove HOTAS for Star Citizen first."},
		{"/extensions/proton", 409, "CachyOS Proton is part of VaporOS and cannot be removed."},
		{"/extensions/nope", 404, `no extension \"nope\"`},
		{"/extensions/star-citizen?purge=yes", 400, "purge must be 0 or 1"},
	} {
		if code, body := r.do("DELETE", c.path, ""); code != c.code || !strings.Contains(body, c.msg) {
			t.Errorf("DELETE %s = %d %s, want %d %q", c.path, code, body, c.code, c.msg)
		}
	}
	if code, _ := r.do("DELETE", "/extensions/sc-hotas", ""); code != 200 {
		t.Fatal("removing sc-hotas")
	}
	if code, _ := r.do("DELETE", "/extensions/star-citizen?purge=0", ""); code != 200 {
		t.Fatal("removing star-citizen once nothing needs it")
	}
	if w := wantedNow(t); len(w) != 0 {
		t.Fatalf("wanted = %v", w)
	}
	if x := r.card("star-citizen"); x.State != StateNotInstalled {
		t.Fatalf("star-citizen added and removed before a restart = %+v", x)
	}
}

// Images that do not fit with the 2 GiB VaporOS keeps free are refused up
// front, as is a disk that cannot seal them.
func TestInstallSpace(t *testing.T) {
	r := newRig(t)
	r.sealer.set(func(f *fakeSealer) { f.free = store.ExtReserve + 1000 })
	code, body := r.do("POST", "/extensions/truckersmp", `{}`)
	if code != 409 || !strings.Contains(body, "There is not enough free space") {
		t.Fatalf("no space: %d %s", code, body)
	}
	r.sealer.set(func(f *fakeSealer) { f.free = store.ExtReserve + 3000 })
	if code, body := r.do("POST", "/extensions/truckersmp", `{}`); code != 200 {
		t.Fatalf("just enough: %d %s", code, body)
	}
	r.s.mu.Lock()
	r.s.noVerity = true
	r.s.mu.Unlock()
	if code, body := r.do("POST", "/extensions/star-citizen", `{"options":{"disk":"/var"}}`); code != 409 || !strings.Contains(body, "cannot seal") {
		t.Fatalf("no verity: %d %s", code, body)
	}
}

// Removing a running extension stops its units now, lets its helper undo
// what it set up, and with purge deletes its data; its settings stay
// without purge.
func TestRemove(t *testing.T) {
	r := newRig(t)
	h := &recHelper{}
	useHelper(t, "coolercontrol", h)
	if code, body := r.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`); code != 200 {
		t.Fatalf("install: %d %s", code, body)
	}
	r.pass()
	r.boot()
	if !slices.Equal(h.Calls(), []string{"install coolercontrol"}) || !isInstalled("coolercontrol") {
		t.Fatalf("after the restart: calls %v, installed %v", h.Calls(), isInstalled("coolercontrol"))
	}
	if !exists(filepath.Join(config.ExtDataDir(), "coolercontrol")) {
		t.Fatal("no system data area")
	}

	if code, body := r.do("DELETE", "/extensions/coolercontrol", ""); code != 200 {
		t.Fatalf("remove: %d %s", code, body)
	}
	if want := []string{"stop -- coolercontrold.service cc-fans@*.service"}; !slices.Equal(r.units, want) {
		t.Errorf("systemctl %q, want %q", r.units, want)
	}
	if !slices.Equal(h.Calls(), []string{"install coolercontrol", "remove coolercontrol"}) || isInstalled("coolercontrol") {
		t.Errorf("calls %v, installed %v", h.Calls(), isInstalled("coolercontrol"))
	}
	if !exists(settingsPath("coolercontrol")) || !exists(filepath.Join(config.ExtDataDir(), "coolercontrol")) {
		t.Error("a removal without purge deleted settings or data")
	}
	r.pass()
	if h.Calls()[len(h.Calls())-1] != "remove coolercontrol" {
		t.Error("a pass set up an extension that was removed")
	}

	// Purge, on an extension with a home area.
	useHelper(t, "truckersmp", h)
	home := filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, "truckersmp")
	must(t, os.MkdirAll(home, 0o755))
	if code, _ := r.do("POST", "/extensions/truckersmp", `{}`); code != 200 {
		t.Fatal("install truckersmp")
	}
	r.pass()
	r.boot()
	if code, body := r.do("DELETE", "/extensions/truckersmp?purge=1", ""); code != 200 {
		t.Fatalf("purge: %d %s", code, body)
	}
	if last := h.Calls()[len(h.Calls())-1]; last != "remove truckersmp purge" {
		t.Errorf("last call %q", last)
	}
	if want := []string{"rm -rf -- " + home}; !slices.Equal(r.gamer, want) {
		t.Errorf("as vapor %q, want %q", r.gamer, want)
	}
}

// A helper's Install runs once per extension mounted for the first time,
// as root after the restart, and only for a wanted one; a failure is tried
// again, the card installing meanwhile, at most maxHelperInstalls times a
// boot, then needs attention until "Try again".
func TestHelperInstall(t *testing.T) {
	r := newRig(t)
	h := &recHelper{installErr: errors.New("api.truckersmp.com did not answer")}
	useHelper(t, "truckersmp", h)
	if code, _ := r.do("POST", "/extensions/truckersmp", `{}`); code != 200 {
		t.Fatal("install")
	}
	r.pass()
	if len(h.Calls()) != 0 {
		t.Fatalf("Install ran before the restart: %v", h.Calls())
	}
	r.boot()
	if x := r.card("truckersmp"); x.State != StateInstalling || x.Reason != "" {
		t.Fatalf("after a failed setup with tries left = %+v", x)
	}
	r.pass()
	r.pass()
	r.pass()
	if n := len(h.Calls()); n != maxHelperInstalls {
		t.Fatalf("%d installs, want %d", n, maxHelperInstalls)
	}
	if x := r.card("truckersmp"); x.State != StateNeedsAttention || x.Reason != "Setting up TruckersMP didn't finish. Try again, or remove it." {
		t.Fatalf("after the last try = %+v", x)
	}
	h.installErr = nil
	if code, body := r.do("POST", "/extensions/truckersmp/retry", ""); code != 200 {
		t.Fatalf("retry: %d %s", code, body)
	}
	r.pass()
	if x := r.card("truckersmp"); x.State != StateInstalled || x.Reason != "" || !isInstalled("truckersmp") {
		t.Fatalf("after Try again = %+v", x)
	}
	r.pass()
	if n := len(h.Calls()); n != maxHelperInstalls+1 {
		t.Fatalf("%d installs: it ran again once set up", n)
	}
}

// On the trial that first mounts it, the helper sets an extension up only
// once `vos health` passed the trial: a trial that fails leaves nothing
// set up for an extension that is not there.
func TestHelperInstallWaitsForTheTrial(t *testing.T) {
	r := newRig(t)
	h := &recHelper{}
	useHelper(t, "truckersmp", h)
	if code, _ := r.do("POST", "/extensions/truckersmp", `{}`); code != 200 {
		t.Fatal("install")
	}
	r.pass()
	p, err := store.Pending()
	must(t, err)
	r.report(store.BootReport{Mode: store.ModePending, Set: p.Name, TriesLeft: 1, Mounted: mountedAs(r.imgs["proton"], r.imgs["truckersmp"])})
	r.s, r.b = r.service()
	r.wire()
	r.pass()
	if len(h.Calls()) != 0 || isInstalled("truckersmp") {
		t.Fatalf("set up during the trial: %v", h.Calls())
	}
	writeFile(t, config.ExtTrialOKPath(), p.Name+"\n") // vos health passed it
	r.pass()
	if !slices.Equal(h.Calls(), []string{"install truckersmp"}) || !isInstalled("truckersmp") {
		t.Fatalf("after the trial passed: %v", h.Calls())
	}
}

// "Try again" forgets the failed fingerprint, so the set is proposed again.
func TestRetryUnblocks(t *testing.T) {
	r := newRig(t)
	r.seal(r.imgs["truckersmp"])
	locked(t, func() error {
		if err := store.WriteWanted([]string{"truckersmp"}); err != nil {
			return err
		}
		return store.AddFailed(store.Fingerprint(store.Pairs(r.b.cat, []string{"proton", "truckersmp"}), nil))
	})
	r.pass()
	if p, _ := store.Pending(); p != nil {
		t.Fatal("proposed a failed set")
	}
	if code, body := r.do("POST", "/extensions/nope/retry", ""); code != 404 {
		t.Fatalf("retry unknown: %d %s", code, body)
	}
	if code, body := r.do("POST", "/extensions/truckersmp/retry", ""); code != 200 {
		t.Fatalf("retry: %d %s", code, body)
	}
	if f, _ := store.Failed(); len(f) != 0 {
		t.Fatalf("failed = %v", f)
	}
	r.pass()
	if p, _ := store.Pending(); p == nil || !slices.Equal(p.IDs, []string{"proton", "truckersmp"}) {
		t.Fatalf("pending = %+v", p)
	}
	if x := r.card("truckersmp"); x.State != StateRestartNeeded {
		t.Fatalf("truckersmp = %+v", x)
	}
}

// Settings are checked against the descriptor; one that feeds kernel
// module options takes the password and makes a new set, which a restart
// tries.
func TestSettings(t *testing.T) {
	r := newRig(t)
	h := &recHelper{options: func(x *Ext) []string {
		if x.Settings["overdrive"] == true {
			return []string{"options amdgpu ppfeaturemask=0xfff7ffff", "options it87 force_id=0x8628"}
		}
		return nil
	}}
	useHelper(t, "coolercontrol", h)
	if code, _ := r.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`); code != 200 {
		t.Fatal("install")
	}
	r.pass()
	r.boot()

	for _, c := range []struct {
		path, body string
		code       int
		msg        string
	}{
		{"/extensions/nope/settings", `{"settings":{}}`, 404, `no extension \"nope\"`},
		{"/extensions/coolercontrol/settings", `{"settings":`, 400, "bad request body"},
		{"/extensions/coolercontrol/settings", `{"settings":{"fan":1}}`, 400, `unknown setting \"fan\"`},
		{"/extensions/coolercontrol/settings", `{"settings":{"poll":"fast"}}`, 400, "poll must be one of normal, slow"},
		{"/extensions/coolercontrol/settings", `{"settings":{"overdrive":"yes"}}`, 400, "overdrive must be true or false"},
		{"/extensions/coolercontrol/settings", `{"settings":{"overdrive":true}}`, 403, "Changing this setting needs the admin password"},
	} {
		if code, body := r.do("PUT", c.path, c.body); code != c.code || !strings.Contains(body, c.msg) {
			t.Errorf("PUT %s %s = %d %s, want %d %q", c.path, c.body, code, body, c.code, c.msg)
		}
	}
	// Not a module option: no password.
	if code, body := r.do("PUT", "/extensions/coolercontrol/settings", `{"settings":{"poll":"normal"}}`); code != 200 {
		t.Fatalf("poll: %d %s", code, body)
	}
	r.pass()
	if p, _ := store.Pending(); p != nil {
		t.Fatalf("a plain setting proposed %+v", p)
	}
	if code, body := r.do("PUT", "/extensions/coolercontrol/settings",
		`{"settings":{"overdrive":true},"password":"`+rigPassword+`"}`); code != 200 {
		t.Fatalf("overdrive: %d %s", code, body)
	}
	r.pass()
	p, err := store.Pending()
	must(t, err)
	// Only the module parameters the descriptor lists reach the set.
	if p == nil || !slices.Equal(p.Options, []string{"options amdgpu ppfeaturemask=0xfff7ffff"}) {
		t.Fatalf("pending = %+v", p)
	}
	doc := r.doc()
	if c := r.card("coolercontrol"); c.State != StateRestartNeeded || !c.Mounted || c.Settings[0].Value != true {
		t.Fatalf("coolercontrol = %+v", c)
	}
	if doc.Restart.Reason != "Restart to finish changing the settings of CoolerControl." {
		t.Fatalf("reason = %q", doc.Restart.Reason)
	}
	r.boot()
	if c := r.card("coolercontrol"); c.State != StateInstalled {
		t.Fatalf("after the restart = %+v", c)
	}
}

func TestActions(t *testing.T) {
	r := newRig(t)
	h := &recHelper{}
	useHelper(t, "truckersmp", h)
	if code, body := r.do("POST", "/extensions/truckersmp/actions/copy-profiles", `{}`); code != 409 || !strings.Contains(body, "TruckersMP is not running yet") {
		t.Fatalf("not mounted: %d %s", code, body)
	}
	if code, _ := r.do("POST", "/extensions/truckersmp", `{}`); code != 200 {
		t.Fatal("install")
	}
	r.pass()
	r.boot()
	for _, c := range []struct {
		path, body string
		code       int
		msg        string
	}{
		{"/extensions/nope/actions/copy-profiles", `{}`, 404, `no extension \"nope\"`},
		{"/extensions/truckersmp/actions/fly", `{}`, 404, `TruckersMP has no action \"fly\"`},
		{"/extensions/truckersmp/actions/copy-profiles", `{"args":[1]}`, 400, "args must be an object"},
		{"/extensions/truckersmp/actions/copy-profiles", `{"args":`, 400, "bad request body"},
	} {
		if code, body := r.do("POST", c.path, c.body); code != c.code || !strings.Contains(body, c.msg) {
			t.Errorf("POST %s %s = %d %s, want %d %q", c.path, c.body, code, body, c.code, c.msg)
		}
	}
	if code, body := r.do("POST", "/extensions/truckersmp/actions/copy-profiles", `{"args":{"game":"ets2"}}`); code != 200 {
		t.Fatalf("action: %d %s", code, body)
	}
	if code, _ := r.do("POST", "/extensions/truckersmp/actions/copy-profiles", ``); code != 200 {
		t.Fatal("action without a body")
	}
	if want := []string{"install truckersmp", `action truckersmp copy-profiles {"game":"ets2"}`, "action truckersmp copy-profiles "}; !slices.Equal(h.Calls(), want) {
		t.Fatalf("calls %q, want %q", h.Calls(), want)
	}
}

func TestSkipOnce(t *testing.T) {
	r := newRig(t)
	if code, body := r.do("POST", "/extensions/skip-once", ""); code != 200 || body != "{}" {
		t.Fatalf("skip-once: %d %s", code, body)
	}
	if !exists(config.ExtSkipOncePath()) || !r.doc().SkipOnce {
		t.Fatal("no skip-once flag")
	}
	for range 2 { // taking it back twice is fine
		if code, body := r.do("DELETE", "/extensions/skip-once", ""); code != 200 || body != "{}" {
			t.Fatalf("taking skip-once back: %d %s", code, body)
		}
	}
	if exists(config.ExtSkipOncePath()) || r.doc().SkipOnce {
		t.Fatal("the skip-once flag stayed")
	}
}

// GET /extensions answers the document; the extension ids "skip-once" and
// friends never reach the install route.
func TestGetDocument(t *testing.T) {
	r := newRig(t)
	code, body := r.do("GET", "/extensions", "")
	var doc Document
	if code != 200 || json.Unmarshal([]byte(body), &doc) != nil || len(doc.Extensions) != 6 {
		t.Fatalf("GET: %d %s", code, body)
	}
}

// pwHelper records the extensions it passed a new admin password to.
type pwHelper struct {
	NopHelper
	mu  sync.Mutex
	got []string
}

func (h *pwHelper) PasswordChanged(_ context.Context, x *Ext) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, x.ID)
	return nil
}

// A new admin password reaches the helpers that keep one, for each
// extension this boot mounted that is still wanted, through the hook the
// routes register with the API server.
func TestPasswordChangedReachesTheHelpers(t *testing.T) {
	r := newRig(t)
	h := &pwHelper{}
	useHelper(t, "coolercontrol", h)
	useHelper(t, "truckersmp", h)
	for _, id := range []string{"coolercontrol", "truckersmp"} {
		if code, _ := r.do("POST", "/extensions/"+id, `{"password":"`+rigPassword+`"}`); code != 200 {
			t.Fatal("install " + id)
		}
	}
	r.pass()
	r.boot()
	if code, _ := r.do("DELETE", "/extensions/truckersmp", ""); code != 200 {
		t.Fatal("remove truckersmp")
	}

	must(t, auth.SetAdminPassword("", rigPassword))
	srv := api.New(api.Options{})
	r.s.Routes(srv)
	send := func(path, body, cookie, csrf string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", api.Prefix+path, strings.NewReader(body))
		req.RemoteAddr, req.Host = "127.0.0.1:40000", "127.0.0.1"
		req.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
			req.Header.Set("X-VOS-CSRF", csrf)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w
	}
	w := send("/auth/login", `{"password":"`+rigPassword+`"}`, "", "")
	var login struct{ CSRF string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &login) != nil || len(w.Result().Cookies()) == 0 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	c := w.Result().Cookies()[0]
	if w := send("/auth/password", `{"current":"`+rigPassword+`","new":"vapor-vapor-2"}`, c.Name+"="+c.Value, login.CSRF); w.Code != 200 {
		t.Fatalf("password: %d %s", w.Code, w.Body)
	}
	r.s.cc.passwords.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()
	if !slices.Equal(h.got, []string{"coolercontrol"}) {
		t.Fatalf("passed to %q, want coolercontrol alone (truckersmp was removed)", h.got)
	}
}
