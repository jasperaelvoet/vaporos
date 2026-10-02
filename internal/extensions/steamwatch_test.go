package extensions

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// loginusers.vdf as Steam writes it, with two accounts.
const loginUsers = `"users"
{
	"76561198012345678"
	{
		"AccountName"		"vaporfan"
		"PersonaName"		"Vapor Fan"
		"RememberPassword"		"1"
		"WantsOfflineMode"		"0"
		"SkipOfflineModeWarning"		"0"
		"AllowAutoLogin"		"1"
		"MostRecent"		"1"
		"Timestamp"		"1759398000"
	}
	"76561198087654321"
	{
		"AccountName"		"kid"
		"PersonaName"		"Kid"
		"RememberPassword"		"1"
		"MostRecent"		"0"
		"Timestamp"		"1759390000"
	}
}
`

// The two accounts' ids (SteamID64 & 0xffffffff).
const (
	acctFan = "52079950"
	acctKid = "127388593"
)

func writePrepareState(t *testing.T, st prepareState) {
	t.Helper()
	// The whole record as prepare writes it; vosd reads only some of it.
	b, err := json.Marshal(map[string]any{
		"fingerprint": "f00d",
		"vos":         bootedVersion,
		"accounts":    st.Accounts,
		"default":     map[string]any{"wrote": "proton-cachyos-slr", "before": nil, "suspended": false},
		"apps":        map[string]any{},
		"shortcuts":   st.Shortcuts,
		"error":       "",
	})
	must(t, err)
	writeFile(t, filepath.Join(config.GamerHome, config.ExtGamerStateFile), string(b))
}

func TestLoginAccounts(t *testing.T) {
	got, err := loginAccounts([]byte(loginUsers))
	if err != nil || !slices.Equal(got, []string{acctKid, acctFan}) {
		t.Fatalf("accounts %q, %v", got, err)
	}
	if got, err := loginAccounts([]byte(`"users" { }`)); err != nil || len(got) != 0 {
		t.Fatalf("none: %q %v", got, err)
	}
	if got, err := loginAccounts([]byte(`"other" { "1" { } }`)); err != nil || len(got) != 0 {
		t.Fatalf("not loginusers: %q %v", got, err)
	}
	if _, err := loginAccounts([]byte(`"users" { "765`)); err == nil {
		t.Fatal("a half-written file parsed")
	}
}

func TestCheckAccounts(t *testing.T) {
	e := newEnv(t)
	s, _ := e.service()
	var restarts []string
	s.SetSteamRestarter(func(reason string) { restarts = append(restarts, reason) })

	s.checkAccounts() // Steam never ran
	writeFile(t, filepath.Join(config.GamerHome, loginUsersRel), loginUsers)
	s.checkAccounts() // prepare never ran: nothing to compare
	if len(restarts) != 0 {
		t.Fatalf("restarts %q", restarts)
	}

	writePrepareState(t, prepareState{Accounts: []string{acctFan}})
	s.checkAccounts()
	s.checkAccounts()
	if len(restarts) != 1 {
		t.Fatalf("one new account, restarts %q", restarts)
	}
	// Prepare set it up after the restart; a third account comes later.
	writePrepareState(t, prepareState{Accounts: []string{acctFan, acctKid}})
	s.checkAccounts()
	if len(restarts) != 1 {
		t.Fatalf("restarts %q", restarts)
	}
	writePrepareState(t, prepareState{Accounts: []string{acctFan}})
	s.checkAccounts()
	if len(restarts) != 2 {
		t.Fatalf("a new account again, restarts %q", restarts)
	}
}

func TestSunshineApps(t *testing.T) {
	steamBox(t)
	scID := ShortcutGameID(ShortcutAppID("star-citizen", "launcher")) // above 2^63
	withHelper(t, "truckersmp", testHelper{steam: SteamParts{SunshineApps: []SunshineApp{
		{Name: "ETS2 Multiplayer", Detached: []string{"/usr/bin/vos ext truckersmp mp ets2"}},
		{Name: "Bad\nname", Detached: []string{"x"}},
		{Name: "No command"},
	}}})
	// Nothing recorded yet: only the helper's own entry.
	if got := SunshineApps(); len(got) != 1 || got[0].Name != "ETS2 Multiplayer" {
		t.Fatalf("apps %+v", got)
	}
	writePrepareState(t, prepareState{Accounts: []string{acctFan, acctKid}, Shortcuts: map[string]map[string]preparedShortcut{
		acctKid: {"star-citizen/launcher": {AppID: int64(int32(uint32(scID >> 32))), GameID: itoa(scID), Deleted: true}},
		acctFan: {"star-citizen/launcher": {AppID: int64(int32(uint32(scID >> 32))), GameID: itoa(scID)}},
	}})
	want := []SunshineApp{
		{Name: "Star Citizen", Detached: []string{"/usr/bin/vos session launch steam://rungameid/" + itoa(scID)}},
		{Name: "ETS2 Multiplayer", Detached: []string{"/usr/bin/vos ext truckersmp mp ets2"}},
	}
	if got := SunshineApps(); !reflect.DeepEqual(got, want) {
		t.Fatalf("apps %+v\nwant %+v", got, want)
	}
	if scID < 1<<63 {
		t.Fatal("the fixture's game id should be above 2^63")
	}
}

func TestPreparedGameID(t *testing.T) {
	own := ShortcutGameID(ShortcutAppID("star-citizen", "launcher"))
	other := ShortcutGameID(0x80001234)
	st := &prepareState{Shortcuts: map[string]map[string]preparedShortcut{
		"1": {"star-citizen/launcher": {GameID: itoa(other)}},
		"2": {"star-citizen/launcher": {GameID: itoa(own)}},
		"3": {"star-citizen/launcher": {GameID: "227300"}}, // not a shortcut's
	}}
	if id, ok := st.gameID("star-citizen", "launcher"); !ok || id != own {
		t.Errorf("game id %d %v, want VaporOS's own", id, ok)
	}
	delete(st.Shortcuts, "2")
	if id, ok := st.gameID("star-citizen", "launcher"); !ok || id != other {
		t.Errorf("game id %d %v, want the lowest account's", id, ok)
	}
	if _, ok := st.gameID("truckersmp", "ets2-mp"); ok {
		t.Error("found a shortcut nobody recorded")
	}
	var none *prepareState
	if _, ok := none.gameID("star-citizen", "launcher"); ok {
		t.Error("found one without a record")
	}
}

func TestSteamExtSettings(t *testing.T) {
	steamBox(t)
	d, err := Shipped("star-citizen")
	must(t, err)
	writeFile(t, filepath.Join(config.ExtSettingsDir(), "star-citizen.json"), `{"library":"/var/mnt/SATA1TB","unknown":true}`)
	x := steamExt("star-citizen", d)
	if !reflect.DeepEqual(x.Settings, map[string]any{"library": "/var/mnt/SATA1TB", "tray": false}) {
		t.Errorf("settings %v", x.Settings)
	}
}

// A helper's entry with its shortcut's name stands for the shortcut:
// Moonlight lists TruckersMP's multiplayer start once.
func TestSunshineAppsDirectEntry(t *testing.T) {
	steamBox(t)
	home := filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, "truckersmp")
	withHelper(t, "truckersmp", testHelper{steam: SteamParts{
		Shortcuts:    map[string]ShortcutTarget{"ets2-mp": {Exe: "/usr/bin/vos", StartDir: home, Args: []string{"ext", "truckersmp", "mp", "ets2"}}},
		SunshineApps: []SunshineApp{{Name: "ETS2 multiplayer", Detached: []string{"/usr/bin/vos ext truckersmp mp ets2"}}},
	}})
	tmpID := ShortcutGameID(ShortcutAppID("truckersmp", "ets2-mp"))
	scID := ShortcutGameID(ShortcutAppID("star-citizen", "launcher"))
	writePrepareState(t, prepareState{Accounts: []string{acctFan}, Shortcuts: map[string]map[string]preparedShortcut{
		acctFan: {
			"truckersmp/ets2-mp":    {AppID: int64(int32(uint32(tmpID >> 32))), GameID: itoa(tmpID)},
			"star-citizen/launcher": {AppID: int64(int32(uint32(scID >> 32))), GameID: itoa(scID)},
		},
	}})
	want := []SunshineApp{
		{Name: "Star Citizen", Detached: []string{"/usr/bin/vos session launch steam://rungameid/" + itoa(scID)}},
		{Name: "ETS2 multiplayer", Detached: []string{"/usr/bin/vos ext truckersmp mp ets2"}},
	}
	if got := SunshineApps(); !reflect.DeepEqual(got, want) {
		t.Fatalf("apps %+v\nwant %+v", got, want)
	}
}
