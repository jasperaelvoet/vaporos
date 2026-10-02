package steam

import (
	"reflect"
	"testing"
)

func TestAppLaunchOptions(t *testing.T) {
	orig := readFixture(t, "localconfig.vdf")
	got, err := AppLaunchOptions([]byte(orig))
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint32]string{
		227300:  "-nointro -64bit",
		1091500: `PROTON_LOG=1 WINEDLLOVERRIDES="dxgi=n,b" %command% -skipStartScreen`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}

	// The first block of a repeated app counts, as in Steam's lookups, and
	// keys that are not app ids are skipped.
	odd := `"UserLocalConfigStore" { "Software" { "Valve" { "Steam" { "Apps" {
		"10" { "Playtime" "1" }
		"10" { "LaunchOptions" "-second" }
		"x" { "LaunchOptions" "-x" }
		"0" { "LaunchOptions" "-zero" }
		"20" "not a block"
		"30" { "launchoptions" "-lower" }
	} } } } }`
	if got, err := AppLaunchOptions([]byte(odd)); err != nil || !reflect.DeepEqual(got, map[uint32]string{30: "-lower"}) {
		t.Errorf("odd: %q %v", got, err)
	}
	if got, err := AppLaunchOptions([]byte(`"UserLocalConfigStore" { }`)); err != nil || len(got) != 0 {
		t.Errorf("no apps: %q %v", got, err)
	}
	if _, err := AppLaunchOptions([]byte(`"UserLocalConfigStore" {`)); err == nil {
		t.Error("broken file accepted")
	}
}

func TestDropEmptyApp(t *testing.T) {
	orig := readFixture(t, "localconfig.vdf")
	added, _, err := SetLaunchOptions([]byte(orig), 270880, "x")
	if err != nil {
		t.Fatal(err)
	}
	emptied, _, err := DeleteLaunchOptions(added, 270880)
	if err != nil {
		t.Fatal(err)
	}
	out, dropped, err := DropEmptyApp(emptied, 270880)
	if err != nil || !dropped || string(out) != orig {
		t.Errorf("%v %v\n%s", dropped, err, out)
	}
	// A block with anything in it stays, as do apps that are not there.
	for _, app := range []uint32{227300, 949230, 270880} {
		if out, dropped, err := DropEmptyApp([]byte(orig), app); err != nil || dropped || string(out) != orig {
			t.Errorf("%d: %v %v", app, dropped, err)
		}
	}
}
