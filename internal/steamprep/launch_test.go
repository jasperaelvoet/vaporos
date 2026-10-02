package steamprep

import (
	"bytes"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

const (
	tokenETS2 = "/usr/bin/vos ext launch --app 227300 "
	tokenATS  = "/usr/bin/vos ext launch --app 270880 "
)

func TestLaunchOptionsEveryAccount(t *testing.T) {
	b := newBox(t)
	lcA := "userdata/52079950/config/localconfig.vdf"
	origA := b.steamFile(lcA)
	b.desire(truckers(proton()))
	b.run(false)
	for _, acct := range []uint32{acctA, acctB} {
		if o, _ := b.launchOptions(acct, ets2); o != tokenETS2+"%command% -nointro -64bit" {
			t.Errorf("%d ETS2: %q", acct, o)
		}
		// ATS was never run on this account: its block is made.
		if o, _ := b.launchOptions(acct, ats); o != tokenATS+"%command%" {
			t.Errorf("%d ATS: %q", acct, o)
		}
		// Apps without a hook stay as they are.
		if o, _ := b.launchOptions(acct, 1091500); o != `PROTON_LOG=1 WINEDLLOVERRIDES="dxgi=n,b" %command% -skipStartScreen` {
			t.Errorf("%d other app: %q", acct, o)
		}
	}
	l := b.state().peekApp(ets2).Launch["52079950"]
	if l == nil || l.Wrote != tokenETS2+"%command% -nointro -64bit" || l.Before != "-nointro -64bit" {
		t.Fatalf("record %+v", l)
	}

	// The user edits the options in Steam around the dispatcher: theirs.
	lcB := "userdata/127388593/config/localconfig.vdf"
	setB := func(opts string) {
		b.edit(lcB, func(d []byte) []byte {
			out, _, err := steam.SetLaunchOptions(d, ets2, opts)
			b.check(err)
			return out
		})
	}
	setB("gamemoderun " + tokenETS2 + "%command% -nointro")
	b.run(false)
	if o, _ := b.launchOptions(acctB, ets2); o != "gamemoderun "+tokenETS2+"%command% -nointro" {
		t.Errorf("edited: %q", o)
	}
	if l := b.state().peekApp(ets2).Launch["127388593"]; l.Before != "gamemoderun %command% -nointro" {
		t.Errorf("before %q", l.Before)
	}
	// ...or drop the dispatcher, which comes back in front of theirs.
	setB("PROTON_LOG=1 %command%")
	b.run(false)
	if o, _ := b.launchOptions(acctB, ets2); o != "PROTON_LOG=1 "+tokenETS2+"%command%" {
		t.Errorf("dropped: %q", o)
	}

	// Without the dispatcher in every bootable VaporOS (steam.json says
	// so), the options lose it: those nobody changed are exactly as before.
	d := truckers(proton())
	d.Dispatcher = false
	b.desire(d)
	b.run(false)
	if got := b.steamFile(lcA); !bytes.Equal(got, origA) {
		t.Errorf("not as before:\n%s", got)
	}
	if o, _ := b.launchOptions(acctB, ets2); o != "PROTON_LOG=1 %command%" {
		t.Errorf("unwrapped: %q", o)
	}
	if a := b.state().peekApp(ets2); a != nil && len(a.Launch) != 0 {
		t.Errorf("records left: %+v", a.Launch)
	}
}

func TestHookGoneUnwraps(t *testing.T) {
	b := newBox(t)
	b.desire(truckers(proton()))
	b.run(false)
	// TruckersMP no longer mounted; and a token for an app VaporOS has no
	// record of (a record that was lost) goes too.
	b.edit("userdata/52079950/config/localconfig.vdf", func(d []byte) []byte {
		out, _, err := steam.SetLaunchOptions(d, 949230, "/usr/bin/vos ext launch --app 949230 %command% -dx12")
		b.check(err)
		return out
	})
	b.desire(proton())
	b.run(false)
	if o, ok := b.launchOptions(acctA, ets2); o != "-nointro -64bit" {
		t.Errorf("ETS2 %q", o)
	} else if o, ok = b.launchOptions(acctA, ats); ok {
		t.Errorf("ATS still has %q", o)
	}
	if o, _ := b.launchOptions(acctA, 949230); o != "%command% -dx12" {
		t.Errorf("stray token: %q", o)
	}
}

func TestSeveralCommandsLeftAlone(t *testing.T) {
	b := newBox(t)
	lcA := "userdata/52079950/config/localconfig.vdf"
	b.edit(lcA, func(d []byte) []byte {
		out, _, err := steam.SetLaunchOptions(d, ets2, "%command% ; %command%")
		b.check(err)
		return out
	})
	b.desire(truckers(proton()))
	b.run(false)
	if o, _ := b.launchOptions(acctA, ets2); o != "%command% ; %command%" {
		t.Errorf("changed: %q", o)
	}
	l := b.state().peekApp(ets2).Launch["52079950"]
	if l == nil || !l.Conflict || l.Wrote != "" || l.Before != "%command% ; %command%" {
		t.Errorf("record %+v", l)
	}
	if o, _ := b.launchOptions(acctB, ets2); o != tokenETS2+"%command% -nointro -64bit" {
		t.Errorf("other account %q", o)
	}

	// Dispatcher tokens in them go, wherever they are; the rest stays.
	b.edit(lcA, func(d []byte) []byte {
		out, _, err := steam.SetLaunchOptions(d, ets2, tokenETS2+"%command% ; /usr/bin/vos ext launch --app 1\t%command% -x")
		b.check(err)
		return out
	})
	b.run(false)
	if o, _ := b.launchOptions(acctA, ets2); o != "%command% ; %command% -x" {
		t.Errorf("tokens left: %q", o)
	}
	if l := b.state().peekApp(ets2).Launch["52079950"]; l == nil || !l.Conflict || l.Before != "%command% ; %command% -x" {
		t.Errorf("record %+v", l)
	}
}

func TestDecideLaunch(t *testing.T) {
	for _, c := range []struct {
		name     string
		cur      string
		has      bool
		l        *LaunchState
		wrap     bool
		want     string
		keep     bool
		wantNext *LaunchState
	}{
		{"none", "", false, nil, true, tokenETS2 + "%command%", true, &LaunchState{Wrote: tokenETS2 + "%command%"}},
		{"none back", tokenETS2 + "%command%", true, &LaunchState{Wrote: tokenETS2 + "%command%"}, false, "", false, nil},
		{"env", "PROTON_LOG=1 %command%", true, nil, true, "PROTON_LOG=1 " + tokenETS2 + "%command%",
			true, &LaunchState{Wrote: "PROTON_LOG=1 " + tokenETS2 + "%command%", Before: "PROTON_LOG=1 %command%"}},
		{"wrapped, record lost", "env -- A=1 " + tokenETS2 + "%command% -x", true, nil, true, "env -- A=1 " + tokenETS2 + "%command% -x",
			true, &LaunchState{Wrote: "env -- A=1 " + tokenETS2 + "%command% -x", Before: "env -- A=1 %command% -x"}},
		{"not ours", "echo x; %command%", true, nil, false, "echo x; %command%", true, nil},
	} {
		got, keep, next := decideLaunch(ets2, c.cur, c.has, c.l, c.wrap)
		if got != c.want || keep != c.keep || (next == nil) != (c.wantNext == nil) || (next != nil && *next != *c.wantNext) {
			t.Errorf("%s: %q %v %+v", c.name, got, keep, next)
		}
	}
}
