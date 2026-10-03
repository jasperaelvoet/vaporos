package display

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

var scrNow = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func TestScreenKeyAndID(t *testing.T) {
	for _, c := range []struct {
		name, mac, ip, want string
	}{
		{"Living room TV", "AA:BB:CC:DD:EE:FF", "192.168.1.40", "Living room TV"},
		{" iPhone ", "", "", "iPhone"},
		{"roth", "AA:BB:CC:DD:EE:FF", "192.168.1.40", "roth@aa:bb:cc:dd:ee:ff"},
		{"Roth", "aa-bb-cc-dd-ee-ff", "", "Roth@aa:bb:cc:dd:ee:ff"},
		{"Moonlight", "", "192.168.1.40", "Moonlight@192.168.1.40"},
		{"roth", "00:00:00:00:00:00", "::ffff:192.168.1.40", "roth@192.168.1.40"},
		{"", "", "fd00::40", "@fd00::40"},
		{"roth", "", "127.0.0.1", ""},
		{"roth", "garbage", "", ""},
		{"unknown", "", "", ""},
	} {
		if got := ScreenKey(c.name, c.mac, c.ip); got != c.want {
			t.Errorf("ScreenKey(%q, %q, %q) = %q, want %q", c.name, c.mac, c.ip, got, c.want)
		}
	}
	sum := sha256.Sum256([]byte("roth@aa:bb:cc:dd:ee:ff"))
	if got, want := ScreenID("roth@aa:bb:cc:dd:ee:ff"), hex.EncodeToString(sum[:])[:12]; got != want || len(got) != 12 {
		t.Errorf("ScreenID = %q, want %q", got, want)
	}
	if ScreenID("") != "" {
		t.Error("a session without a key has an id")
	}
}

// TestScreensKeyFor: a generic name at an address several devices share
// has no key, and a second IPv4 device behind a MAC that another took
// first gets a screen of its own, while the first keeps its own.
func TestScreensKeyFor(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:ff"
	first, second := "192.168.1.40", "192.168.1.50"
	s := NewScreens()
	if k := s.KeyFor("roth", mac, first, scrNow); k != "roth@"+mac {
		t.Fatalf("first device: %q", k)
	}
	s.Touch(ScreenID("roth@"+mac), "roth", mac, first, scrMode(2796, 1290, 120), scrNow)
	s.Update(ScreenID("roth@"+mac), func(sc *Screen) { sc.Size = 1.3 })
	if k := s.KeyFor("roth", mac, first, scrNow.Add(time.Hour)); k != "roth@"+mac {
		t.Errorf("the first device again: %q", k)
	}
	if k := s.KeyFor("roth", mac, second, scrNow.Add(time.Hour)); k != "roth@"+mac+"@"+second {
		t.Errorf("a second device behind the MAC: %q", k)
	}
	if k := s.KeyFor("roth", mac, "fd00::50", scrNow.Add(time.Hour)); k != "roth@"+mac {
		t.Errorf("an IPv6 address splits nothing: %q", k)
	}
	if k := s.KeyFor("roth", mac, second, scrNow.Add(8*24*time.Hour)); k != "roth@"+mac {
		t.Errorf("long after the first device: %q", k)
	}
	if k := s.KeyFor("iPad", mac, second, scrNow.Add(time.Hour)); k != "iPad" {
		t.Errorf("a name of its own: %q", k)
	}
	if sc, _ := s.Get(ScreenID("roth@" + mac)); sc.IP != first || sc.Size != 1.3 {
		t.Errorf("the first device's screen: %+v", sc)
	}
}

// TestScreensSharedAddr: devices behind a router that NATs them share its
// address and MAC; once two names were seen there, nothing kept for that
// address is any one device's.
func TestScreensSharedAddr(t *testing.T) {
	const router, other = "aa:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:01"
	at := "192.168.1.2"
	hint := Hint{Kind: KindPhone, W: 430, H: 932, DPR: 3, Touch: 5, At: scrNow}
	s := NewScreens()
	s.Touch(ScreenID("Laptop"), "Laptop", router, at, scrMode(1920, 1200, 60), scrNow)
	if !s.PutHint(router, at, hint) || s.HintFor(router, at, scrNow) == nil {
		t.Fatal("one device at the address: its hint was refused")
	}
	if k := s.KeyFor("roth", router, at, scrNow); k != "roth@"+router {
		t.Errorf("one device at the address: key %q", k)
	}
	// A second name at the same address and MAC.
	s.Touch(ScreenID("iPhone"), "iPhone", router, at, scrMode(2796, 1290, 120), scrNow.Add(time.Minute))
	now := scrNow.Add(time.Hour)
	if !s.sharedAddr(router, at, now) || !s.sharedAddr("", at, now) {
		t.Fatal("two names at one address is no shared address")
	}
	if h := s.HintFor(router, at, now); h != nil {
		t.Errorf("a hint at a shared address: %+v", h)
	}
	if s.PutHint(router, at, Hint{Kind: KindLaptop, At: now}) || s.PutPick(router, at, KindTV, now) {
		t.Error("stored a hint or a pick at a shared address")
	}
	if h := s.Hints[router]; h.Kind != KindPhone || h.You {
		t.Errorf("the stored hint changed: %+v", h)
	}
	if k := s.KeyFor("roth", router, at, now); k != "" {
		t.Errorf("a generic name at a shared address: key %q", k)
	}
	if k := s.KeyFor("iPad", router, at, now); k != "iPad" {
		t.Errorf("a name of its own at a shared address: key %q", k)
	}
	// Another MAC at that address (a device that took it over by DHCP),
	// another address, IPv6, or names seen long ago: no sharing.
	if s.sharedAddr(other, at, now) || s.sharedAddr(router, "192.168.1.3", now) || s.sharedAddr(router, "fd00::2", now) ||
		s.sharedAddr(router, at, scrNow.Add(8*24*time.Hour)) {
		t.Error("shared without two names at that address and MAC lately")
	}
	// One name in two cases is one device.
	s = NewScreens()
	s.Touch(ScreenID("roth@"+router), "roth", router, at, scrMode(1920, 1080, 60), scrNow)
	s.Touch(ScreenID("Roth "), "Roth ", router, at, scrMode(1920, 1080, 60), scrNow)
	if s.sharedAddr(router, at, scrNow) {
		t.Error("one name counted twice")
	}
}

func TestScreensLoadSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "screens.json")
	s, err := LoadScreens(path)
	if err != nil || len(s.ByID) != 0 || len(s.Hints) != 0 {
		t.Fatalf("missing file: %+v, %v", s, err)
	}
	id := ScreenID("iPhone")
	s.Touch(id, "iPhone", "aa:bb:cc:dd:ee:ff", "192.168.1.40", scrMode(2796, 1290, 120), scrNow)
	s.Update(id, func(sc *Screen) { sc.Guess, sc.GuessFrom, sc.UIScale, sc.GameDPI = KindPhone, FromName, 2.7, 168 })
	s.PutHint("aa:bb:cc:dd:ee:ff", "192.168.1.40", Hint{Kind: KindPhone, W: 430, H: 932, DPR: 3, Touch: 5, At: scrNow})
	s.HandbackPending = true
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", fi.Mode(), err)
	}
	got, err := LoadScreens(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, s)
	}
	b, _ := os.ReadFile(path)
	for _, k := range []string{`"screens"`, `"hints"`, `"handback_pending": true`, `"steam_auto"`, `"guess_from": "name"`, `"modes": [`, `"ui_scale": 2.7`, `"game_dpi": 168`, `"last_seen"`, `"dpr": 3`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("screens.json lacks %s:\n%s", k, b)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
}

func TestScreensBadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "screens.json")
	os.WriteFile(path+".bad", []byte("older"), 0o600)
	os.WriteFile(path, []byte(`{"screens":{"x":`), 0o600)
	s, err := LoadScreens(path)
	if err == nil || !strings.Contains(err.Error(), "screens.json.bad") {
		t.Errorf("err = %v", err)
	}
	if s == nil || s.ByID == nil || s.Hints == nil || len(s.ByID) != 0 {
		t.Fatalf("not a fresh set: %+v", s)
	}
	if b, _ := os.ReadFile(path + ".bad"); string(b) != `{"screens":{"x":` {
		t.Errorf(".bad = %q", b)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the bad file is still in place: %v", err)
	}
	// It is replaced on the next save.
	s.Touch("abc", "roth", "", "192.168.1.9", scrMode(1920, 1080, 60), scrNow)
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadScreens(path); err != nil || len(got.ByID) != 1 {
		t.Errorf("after save: %+v, %v", got, err)
	}
}

func TestScreensNormalisesOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "screens.json")
	os.WriteFile(path, []byte(`{"screens":{
		"a":{"name":"x","kind":"desk","size":0,"guess":"blob","guess_from":"name","modes":["1x1@1","2x2@2","3x3@3","4x4@4","5x5@5"]},
		"b":{"name":"y","kind":"tv","size":9}},
		"hints":{"fd00::1":{"kind":"phone"},"":{"kind":"tv"},"AA:BB:CC:DD:EE:01":{"kind":"tv"},"aa:bb:cc:dd:ee:ff":{"kind":"what"},"192.168.1.4":{"kind":"tv"}}}`), 0o600)
	s, err := LoadScreens(path)
	if err != nil {
		t.Fatal(err)
	}
	a, b := s.ByID["a"], s.ByID["b"]
	if a.Kind != "" || a.Size != 1 || a.Guess != "" || len(a.Modes) != maxScreenModes || b.Kind != KindTV || b.Size != SizeMax {
		t.Errorf("screens: %+v %+v", a, b)
	}
	if _, ok := s.Hints["fd00::1"]; ok || len(s.Hints) != 2 || s.Hints["aa:bb:cc:dd:ee:ff"].Kind != KindUnknown {
		t.Errorf("hints: %+v", s.Hints)
	}
}

func TestScreensTouch(t *testing.T) {
	s := NewScreens()
	id := ScreenID(ScreenKey("roth", "aa:bb:cc:dd:ee:ff", "192.168.1.40"))
	modes := []string{"1280x720@60", "2796x1290@120", "1920x1080@60", "1280x720@60", "2442x1227@60", "3840x2160@60"}
	for i, m := range modes {
		md, err := edid.ParseMode(m)
		if err != nil {
			t.Fatal(err)
		}
		s.Touch(id, "roth", "AA:BB:CC:DD:EE:FF", "192.168.1.40", md, scrNow.Add(time.Duration(i)*time.Minute))
	}
	sc, ok := s.Get(id)
	if !ok {
		t.Fatal("no screen")
	}
	if want := []string{"3840x2160@60", "2442x1227@60", "1280x720@60", "1920x1080@60"}; !reflect.DeepEqual(sc.Modes, want) {
		t.Errorf("modes = %v, want %v", sc.Modes, want)
	}
	if sc.Size != 1 || sc.MAC != "aa:bb:cc:dd:ee:ff" || sc.IP != "192.168.1.40" || !sc.LastSeen.Equal(scrNow.Add(5*time.Minute)) {
		t.Errorf("screen = %+v", sc)
	}
	if h := sc.History(); len(h) != 4 || h[0] != scrMode(3840, 2160, 60) {
		t.Errorf("history = %v", h)
	}
	// A session without a MAC keeps the one it had.
	s.Touch(id, "roth", "", "::ffff:192.168.1.41", scrMode(1280, 720, 60), scrNow.Add(time.Hour))
	if sc, _ := s.Get(id); sc.MAC != "aa:bb:cc:dd:ee:ff" || sc.IP != "192.168.1.41" {
		t.Errorf("after a session without a MAC: %+v", sc)
	}
	// Get hands out copies.
	sc, _ = s.Get(id)
	sc.Modes[0] = "changed"
	if again, _ := s.Get(id); again.Modes[0] == "changed" {
		t.Error("Get shares the modes slice")
	}
}

func TestScreensEvictAndExpire(t *testing.T) {
	s := NewScreens()
	for i := range maxScreens + 3 {
		s.Touch(fmt.Sprintf("name%02d", i), fmt.Sprintf("Device %02d", i), "", "", scrMode(1920, 1080, 60), scrNow.Add(time.Duration(i)*time.Second))
	}
	if len(s.ByID) != maxScreens {
		t.Fatalf("%d screens", len(s.ByID))
	}
	for i := range 3 {
		if _, ok := s.Get(fmt.Sprintf("name%02d", i)); ok {
			t.Errorf("name%02d, among the least recently seen, survived", i)
		}
	}
	for i := range maxHints + 2 {
		s.PutHint("", fmt.Sprintf("192.168.2.%d", i+1), Hint{Kind: KindPhone, At: scrNow.Add(time.Duration(i) * time.Second)})
	}
	if len(s.Hints) != maxHints {
		t.Fatalf("%d hints", len(s.Hints))
	}
	if _, ok := s.Hints["192.168.2.1"]; ok {
		t.Error("the oldest hint survived")
	}

	s = NewScreens()
	ipID, macID, nameID := ScreenID("roth@192.168.1.40"), ScreenID("roth@aa:bb:cc:dd:ee:ff"), ScreenID("TV")
	s.Touch(ipID, "roth", "", "192.168.1.40", scrMode(1920, 1080, 60), scrNow)
	s.Touch(macID, "roth", "aa:bb:cc:dd:ee:ff", "192.168.1.41", scrMode(1920, 1080, 60), scrNow)
	s.Touch(nameID, "TV", "", "192.168.1.42", scrMode(1920, 1080, 60), scrNow)
	s.PutHint("aa:bb:cc:dd:ee:ff", "192.168.1.41", Hint{Kind: KindPhone, At: scrNow})
	s.PutHint("", "192.168.1.40", Hint{Kind: KindTablet, At: scrNow})
	if v := s.Views(nil, scrNow.Add(8*24*time.Hour)); len(v) != 2 {
		t.Errorf("views a week later: %+v", v)
	}
	s.Prune(scrNow.Add(25 * time.Hour))
	if _, ok := s.Hints["192.168.1.40"]; ok || len(s.Hints) != 1 || len(s.ByID) != 3 {
		t.Errorf("a day later: hints %v, %d screens", s.Hints, len(s.ByID))
	}
	s.Prune(scrNow.Add(8 * 24 * time.Hour))
	if _, ok := s.ByID[ipID]; ok || len(s.ByID) != 2 {
		t.Errorf("a week later the name@ip screen is still there: %v", s.ByID)
	}
	s.Prune(scrNow.Add(181 * 24 * time.Hour))
	if len(s.Hints) != 0 || len(s.ByID) != 2 {
		t.Errorf("half a year later: hints %v, %d screens", s.Hints, len(s.ByID))
	}
}

func TestScreensHints(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:ff"
	phone := Hint{Kind: KindPhone, W: 430, H: 932, DPR: 3, Touch: 5, At: scrNow}
	t.Run("by MAC, over IPv6 too", func(t *testing.T) {
		s := NewScreens()
		if !s.PutHint(mac, "fe80::1%wlan0", phone) {
			t.Fatal("not stored")
		}
		if h := s.HintFor(strings.ToUpper(mac), "192.168.1.40", scrNow.Add(time.Hour)); h == nil || h.Kind != KindPhone || h.IP != "" {
			t.Errorf("HintFor = %+v", h)
		}
		if h := s.HintFor(mac, "192.168.1.40", scrNow.Add(181*24*time.Hour)); h != nil {
			t.Errorf("an expired hint: %+v", h)
		}
	})
	t.Run("never by IPv6 or loopback alone", func(t *testing.T) {
		s := NewScreens()
		if s.PutHint("", "fd00::40", phone) || s.PutHint("", "127.0.0.1", phone) || s.PutHint("", "::1", phone) || len(s.Hints) != 0 {
			t.Errorf("stored: %v", s.Hints)
		}
	})
	t.Run("by IP for a day", func(t *testing.T) {
		s := NewScreens()
		s.PutHint("", "::ffff:192.168.1.40", phone)
		if h := s.HintFor("", "192.168.1.40", scrNow.Add(23*time.Hour)); h == nil || h.IP != "192.168.1.40" {
			t.Errorf("HintFor = %+v", h)
		}
		// The device's MAC became known since: the IP hint still serves.
		if h := s.HintFor(mac, "192.168.1.40", scrNow.Add(time.Hour)); h == nil {
			t.Error("no fallback to the IP hint")
		}
		if h := s.HintFor("", "192.168.1.40", scrNow.Add(25*time.Hour)); h != nil {
			t.Errorf("an IP hint after a day: %+v", h)
		}
	})
	t.Run("a MAC several devices share", func(t *testing.T) {
		s := NewScreens()
		s.PutHint(mac, "192.168.1.40", phone)
		if h := s.HintFor(mac, "192.168.1.40", scrNow); h == nil {
			t.Error("the hint's own device lost it")
		}
		if h := s.HintFor(mac, "192.168.1.50", scrNow); h != nil {
			t.Errorf("another address behind the MAC got the hint: %+v", h)
		}
		// Long ago, another address is no sign of sharing.
		if h := s.HintFor(mac, "192.168.1.50", scrNow.Add(8*24*time.Hour)); h == nil {
			t.Error("an old address still counts")
		}
		// A hint from IPv6 says nothing about its IPv4 address, but a
		// screen under the same MAC elsewhere does.
		s = NewScreens()
		s.PutHint(mac, "fd00::40", phone)
		if h := s.HintFor(mac, "192.168.1.50", scrNow); h == nil {
			t.Error("an IPv6 hint was refused")
		}
		s.Touch(ScreenID("TV"), "TV", mac, "192.168.1.60", scrMode(3840, 2160, 60), scrNow)
		if h := s.HintFor(mac, "192.168.1.50", scrNow); h != nil {
			t.Errorf("the MAC is shared, yet: %+v", h)
		}
	})
	t.Run("a pick at pairing", func(t *testing.T) {
		s := NewScreens()
		s.PutHint(mac, "192.168.1.40", phone)
		if !s.PutPick(mac, "192.168.1.40", KindTablet, scrNow.Add(time.Minute)) {
			t.Fatal("pick not stored")
		}
		h := s.HintFor(mac, "192.168.1.40", scrNow.Add(time.Hour))
		if h == nil || h.Kind != KindTablet || !h.You || h.W != 430 || h.DPR != 3 {
			t.Fatalf("after the pick: %+v", h)
		}
		// A later browser visit keeps the pick and updates the screen.
		s.PutHint(mac, "192.168.1.40", Hint{Kind: KindPhone, W: 390, H: 844, DPR: 3, At: scrNow.Add(time.Hour)})
		if h := s.HintFor(mac, "192.168.1.40", scrNow.Add(time.Hour)); h == nil || h.Kind != KindTablet || !h.You || h.W != 390 {
			t.Errorf("after a later hint: %+v", h)
		}
		if s.PutPick(mac, "192.168.1.40", KindUnknown, scrNow) || s.PutPick("", "fd00::1", KindTV, scrNow) {
			t.Error("a pick of unknown, or of an IPv6 address, was stored")
		}
		// The device's first session with a key takes the pick, once; the
		// browser's screen stays, its kind is unknown until it tells again.
		if k, ok := s.takePick(mac, "192.168.1.40", scrNow.Add(time.Hour)); !ok || k != KindTablet {
			t.Errorf("takePick = %q %v", k, ok)
		}
		if h := s.HintFor(mac, "192.168.1.40", scrNow.Add(time.Hour)); h == nil || h.You || h.Kind != KindUnknown || h.W != 390 {
			t.Errorf("after the pick was taken: %+v", h)
		}
		if k, ok := s.takePick(mac, "192.168.1.40", scrNow.Add(time.Hour)); ok {
			t.Errorf("taken twice: %q", k)
		}
		s.PutHint(mac, "192.168.1.40", Hint{Kind: KindTablet, W: 820, H: 1180, DPR: 2, At: scrNow.Add(2 * time.Hour)})
		if h := s.HintFor(mac, "192.168.1.40", scrNow.Add(2*time.Hour)); h == nil || h.You || h.Kind != KindTablet {
			t.Errorf("the browser's next hint: %+v", h)
		}
	})
}

func TestScreensEdit(t *testing.T) {
	str := func(s string) *string { return &s }
	num := func(f float64) *float64 { return &f }
	yes, no := true, false
	id := ScreenID("roth@192.168.1.40")
	fresh := func() *Screens {
		s := NewScreens()
		s.Touch(id, "roth", "", "192.168.1.40", scrMode(1280, 720, 60), scrNow)
		s.Update(id, func(sc *Screen) { sc.Guess, sc.GuessFrom = KindUnknown, FromDefault })
		return s
	}
	for _, c := range []struct {
		what     string
		before   func(*Screen)
		edit     ScreenEdit
		inEffect Kind
		kind     Kind
		size     float64
		auto     bool
		err      error
	}{
		{"a kind", nil, ScreenEdit{Kind: str("phone")}, "", KindPhone, 1, false, nil},
		{"a new kind resets the size", func(sc *Screen) { sc.Kind, sc.Size = KindTV, 1.3 }, ScreenEdit{Kind: str("phone")}, "", KindPhone, 1, false, nil},
		{"the same kind keeps it", func(sc *Screen) { sc.Kind, sc.Size = KindPhone, 1.3 }, ScreenEdit{Kind: str("phone")}, "", KindPhone, 1.3, false, nil},
		{"a kind and a size", func(sc *Screen) { sc.Kind = KindTV }, ScreenEdit{Kind: str("phone"), Size: num(0.9)}, "", KindPhone, 0.9, false, nil},
		{"auto", func(sc *Screen) { sc.Kind, sc.Size = KindTV, 1.3 }, ScreenEdit{Kind: str("auto")}, "", "", 1, false, nil},
		{"a size pins the kind in effect", nil, ScreenEdit{Size: num(1.1)}, KindTV, KindTV, 1.1, false, nil},
		{"or the guess", nil, ScreenEdit{Size: num(1.1)}, "", KindUnknown, 1.1, false, nil},
		{"a size keeps a pick", func(sc *Screen) { sc.Kind = KindLaptop }, ScreenEdit{Size: num(0.7)}, KindTV, KindLaptop, 0.7, false, nil},
		{"a size ends Steam's own", func(sc *Screen) { sc.SteamAuto = true }, ScreenEdit{Size: num(1.2)}, KindTV, KindTV, 1.2, false, nil},
		{"Steam's own", nil, ScreenEdit{SteamAuto: &yes}, "", "", 1, true, nil},
		{"Steam's own with a kind", nil, ScreenEdit{Kind: str("tv"), SteamAuto: &yes}, "", KindTV, 1, true, nil},
		{"not Steam's own", func(sc *Screen) { sc.SteamAuto = true }, ScreenEdit{SteamAuto: &no}, "", "", 1, false, nil},
		{"bad kind", nil, ScreenEdit{Kind: str("unknown")}, "", "", 1, false, ErrBadKind},
		{"bad size", nil, ScreenEdit{Size: num(2.6)}, "", "", 1, false, ErrBadSize},
		{"small size", nil, ScreenEdit{Size: num(0.39)}, "", "", 1, false, ErrBadSize},
	} {
		s := fresh()
		if c.before != nil {
			s.Update(id, c.before)
		}
		sc, err := s.Edit(id, c.edit, c.inEffect, false)
		if err != c.err {
			t.Errorf("%s: err = %v, want %v", c.what, err, c.err)
			continue
		}
		stored, _ := s.Get(id)
		if err == nil && !reflect.DeepEqual(sc, stored) {
			t.Errorf("%s: returned %+v, stored %+v", c.what, sc, stored)
		}
		if stored.Kind != c.kind || stored.Size != c.size || stored.SteamAuto != c.auto {
			t.Errorf("%s: kind %q size %v steam_auto %v; want %q %v %v", c.what, stored.Kind, stored.Size, stored.SteamAuto, c.kind, c.size, c.auto)
		}
	}
	// The kind in effect is this session's veto: never stored, so a size
	// alone leaves the kind automatic (not even the guess is pinned).
	s := fresh()
	if sc, err := s.Edit(id, ScreenEdit{Size: num(1.1)}, KindTV, true); err != nil || sc.Kind != "" || sc.Size != 1.1 {
		t.Errorf("a size in a vetoed session: %+v %v", sc, err)
	}
	if sc, err := s.Edit(id, ScreenEdit{Kind: str("tv")}, KindTV, true); err != nil || sc.Kind != KindTV {
		t.Errorf("a kind picked in a vetoed session: %+v %v", sc, err)
	}
	if _, err := fresh().Edit("nope", ScreenEdit{Kind: str("tv")}, "", false); err != ErrNoScreen {
		t.Errorf("unknown id: %v", err)
	}
	if _, err := fresh().Edit("nope", ScreenEdit{Kind: str("desk")}, "", false); err != ErrBadKind {
		t.Errorf("a bad body is checked before the id: %v", err)
	}
	s = fresh()
	if !s.Delete(id) || s.Delete(id) || len(s.ByID) != 0 {
		t.Error("Delete")
	}
}

func TestScreensViews(t *testing.T) {
	s := NewScreens()
	if v := s.Views(nil, scrNow); v == nil || len(v) != 0 {
		t.Errorf("empty views = %#v", v)
	}
	tv, phone := ScreenID("TV"), ScreenID("roth@aa:bb:cc:dd:ee:ff")
	s.Touch(tv, "TV", "", "192.168.1.10", scrMode(3840, 2160, 60), scrNow)
	s.Touch(phone, "roth", "aa:bb:cc:dd:ee:ff", "192.168.1.40", scrMode(2796, 1290, 120), scrNow.Add(time.Minute))
	s.Update(phone, func(sc *Screen) { sc.Guess, sc.GuessFrom, sc.UIScale, sc.GameDPI = KindPhone, FromResolution, 2.7, 168 })
	s.Update(tv, func(sc *Screen) { sc.Kind, sc.Size, sc.Guess, sc.GuessFrom = KindTV, 1.1, KindTV, FromName })

	v := s.Views(nil, scrNow)
	want := []ScreenView{
		{ID: phone, Name: "roth", Kind: KindPhone, KindFrom: FromResolution, Guess: KindPhone, Size: 1, Mode: "2796x1290@120", LastSeen: scrNow.Add(time.Minute), UIScale: 2.7, GameDPI: 168, Savable: true},
		{ID: tv, Name: "TV", Kind: KindTV, KindFrom: FromYou, Guess: KindTV, Size: 1.1, Mode: "3840x2160@60", LastSeen: scrNow, Savable: true},
	}
	if !reflect.DeepEqual(v, want) {
		t.Errorf("views:\n got %+v\nwant %+v", v, want)
	}

	// The session streaming now replaces its stored view...
	live := ScreenView{ID: tv, Name: "TV", Kind: KindTV, KindFrom: FromStream, Guess: KindTV, Size: 1.1, Mode: "3840x2160@60", LastSeen: scrNow, UIScale: 2.55, GameDPI: 192, Savable: true}
	if v := s.Views(&live, scrNow); len(v) != 2 || v[1] != live {
		t.Errorf("with the live screen: %+v", v)
	}
	// ...or, for a device that cannot be told apart, comes first.
	anon := ScreenView{Name: "roth", Kind: KindUnknown, KindFrom: FromDefault, Guess: KindUnknown, Size: 1, Mode: "1920x1080@60", LastSeen: scrNow, Savable: true}
	v = s.Views(&anon, scrNow)
	if len(v) != 3 || v[0].ID != "" || v[0].Savable || v[1].ID != phone {
		t.Errorf("with a keyless session: %+v", v)
	}
	if r := live.Ref(); r != (ScreenRef{ID: tv, Name: "TV", Kind: KindTV, KindFrom: FromStream, UIScale: 2.55, GameDPI: 192}) {
		t.Errorf("Ref = %+v", r)
	}
	// A screen never inferred yet.
	if v := (Screen{Name: "x", Size: 1}).View("id1"); v.Kind != KindUnknown || v.KindFrom != FromDefault || v.Guess != KindUnknown {
		t.Errorf("bare view = %+v", v)
	}
}
