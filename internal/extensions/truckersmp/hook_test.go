package truckersmp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/extensions"
)

// Command lines as Steam hands them to `vos ext launch` (%command%, with
// or without its reaper in front) for ETS2 and ATS through Proton
// CachyOS in Steam Linux Runtime 4.
const (
	steamHome = "/var/home/vapor/.local/share/Steam"
	reaper    = steamHome + "/ubuntu12_32/reaper"
	wrapper   = steamHome + "/ubuntu12_32/steam-launch-wrapper"
	slr       = steamHome + "/steamapps/common/SteamLinuxRuntime_4/_v2-entry-point"
	protonBin = "/usr/share/steam/compatibilitytools.d/proton-cachyos-slr/proton"
	ets2Dir   = "/var/mnt/SATA1TB/SteamLibrary/steamapps/common/Euro Truck Simulator 2"
	atsDir    = steamHome + "/steamapps/common/American Truck Simulator"
	injector  = "/var/home/vapor/.local/share/vaporos/ext/truckersmp/bin/truckersmp-cli.exe"
	modDir    = "/var/home/vapor/.local/share/vaporos/ext/truckersmp/files"
)

func words(s string) []string { return strings.Split(s, "|") }

func TestRewriteArgv(t *testing.T) {
	chain := slr + "|--verb=waitforexitandrun|--|" + protonBin + "|waitforexitandrun|"
	for _, c := range []struct {
		name string
		g    game
		in   string
		want string // "" when untouched
	}{
		{"reaper line, no arguments", games[0],
			reaper + "|SteamLaunch|AppId=227300|--|" + wrapper + "|--|" + chain + ets2Dir + "/bin/win_x64/eurotrucks2.exe",
			reaper + "|SteamLaunch|AppId=227300|--|" + wrapper + "|--|" + chain + injector + "|" + ets2Dir + "|" + modDir + "|-rdevice|gl|-nointro|-64bit"},
		{"the tool chain alone, the user's arguments kept", games[0],
			chain + ets2Dir + "/bin/win_x64/eurotrucks2.exe|-nointro|-mm_max_tmp_buffers_size|1000",
			chain + injector + "|" + ets2Dir + "|" + modDir + "|-nointro|-mm_max_tmp_buffers_size|1000"},
		{"ATS in the home's library", games[1],
			reaper + "|SteamLaunch|AppId=270880|--|" + wrapper + "|--|" + chain + atsDir + "/bin/win_x64/amtrucks.exe",
			reaper + "|SteamLaunch|AppId=270880|--|" + wrapper + "|--|" + chain + injector + "|" + atsDir + "|" + modDir + "|-rdevice|gl|-nointro|-64bit"},
		{"a launch option prefix", games[0],
			"/usr/bin/gamemoderun|" + chain + ets2Dir + "/Bin/Win_x64/EuroTrucks2.exe",
			"/usr/bin/gamemoderun|" + chain + injector + "|" + ets2Dir + "|" + modDir + "|-rdevice|gl|-nointro|-64bit"},
		{"the Linux build", games[0], reaper + "|SteamLaunch|AppId=227300|--|" + wrapper + "|--|" +
			steamHome + "/steamapps/common/SteamLinuxRuntime_sniper/_v2-entry-point|--verb=waitforexitandrun|--|" + ets2Dir + "/bin/linux_x64/eurotrucks2", ""},
		{"an install step", games[0], reaper + "|SteamLaunch|AppId=227300|Install=1|--|" + wrapper + "|--|" +
			steamHome + "/legacycompat/iscriptevaluator.exe|--get-current-step|227300", ""},
		{"a relative path", games[0], chain + "bin/win_x64/eurotrucks2.exe", ""},
		{"the other game's executable", games[1], chain + ets2Dir + "/bin/win_x64/eurotrucks2.exe", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			argv := words(c.in)
			i := exeIndex(argv, c.g)
			if c.want == "" {
				if i >= 0 {
					t.Fatalf("found %q", argv[i])
				}
				return
			}
			if i < 0 {
				t.Fatal("no executable found")
			}
			if got := rewriteArgv(argv, i, injector, modDir); !slices.Equal(got, words(c.want)) {
				t.Errorf("got\n%q\nwant\n%q", got, words(c.want))
			}
			if !slices.Equal(argv, words(c.in)) {
				t.Error("the input changed")
			}
		})
	}
}

// syncedBox is a box whose mod files a sync wrote for the games given.
func syncedBox(t *testing.T, keys ...string) *box {
	b := newBox(t)
	srv := newTMPServer(t)
	s, _ := testSyncer(srv, keys...)
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return b
}

func ets2Launch() *extensions.Launch {
	return &extensions.Launch{App: 227300, Argv: words(slr + "|--verb=waitforexitandrun|--|" + protonBin + "|waitforexitandrun|" +
		ets2Dir + "/bin/win_x64/eurotrucks2.exe"), Env: []string{"SteamAppId=227300"}}
}

func TestLaunchHook(t *testing.T) {
	b := syncedBox(t, "ets2")
	h := newHelper()
	ctx := context.Background()

	// No flag: single-player, untouched.
	l := ets2Launch()
	if err := h.LaunchHook(ctx, l); err != nil || !slices.Equal(l.Argv, ets2Launch().Argv) {
		t.Fatalf("without a flag: %v %q", err, l.Argv)
	}

	// The flag of `mp ets2`: multiplayer, once.
	if _, err := writeFlag(flagPath(), games[0], now()); err != nil {
		t.Fatal(err)
	}
	l = ets2Launch()
	if err := h.LaunchHook(ctx, l); err != nil {
		t.Fatal(err)
	}
	inj := filepath.Join(homeDir(), binRel)
	want := append(ets2Launch().Argv[:5:5], inj, ets2Dir, filepath.Join(homeDir(), filesRel), "-rdevice", "gl", "-nointro", "-64bit")
	if !slices.Equal(l.Argv, want) {
		t.Errorf("got\n%q\nwant\n%q", l.Argv, want)
	}
	if read(t, inj) != string(b.injector) {
		t.Error("the injector was not copied into the home")
	}
	l = ets2Launch()
	if err := h.LaunchHook(ctx, l); err != nil || !slices.Equal(l.Argv, ets2Launch().Argv) {
		t.Fatalf("a second start: %v %q", err, l.Argv)
	}

	// ATS's flag leaves ETS2 alone; ATS's files are missing, so its start
	// is refused rather than run single-player.
	writeFlag(flagPath(), games[1], now())
	l = ets2Launch()
	if err := h.LaunchHook(ctx, l); err != nil || !slices.Equal(l.Argv, ets2Launch().Argv) {
		t.Fatalf("ETS2 with ATS's flag: %v %q", err, l.Argv)
	}
	l = &extensions.Launch{App: 270880, Argv: words(protonBin + "|waitforexitandrun|" + atsDir + "/bin/win_x64/amtrucks.exe")}
	err := h.LaunchHook(ctx, l)
	if code, text := refused(err); !errors.Is(err, errStale) || code != "no-files-ats" ||
		text != "TruckersMP didn't start because its files for ATS aren't downloaded yet. Try again once its card in VaporOS says it's ready." {
		t.Fatalf("ATS without files: %v", err)
	}
	if _, err := os.Stat(flagPath()); !os.IsNotExist(err) {
		t.Error("a refused start leaves its flag")
	}
	// ETS2's files changed since the sync checked them.
	core := filepath.Join(homeDir(), filesRel, "core_ets2mp.dll")
	saved := read(t, core)
	fi, _ := os.Stat(core)
	write(t, core, "changed")
	writeFlag(flagPath(), games[0], now())
	err = h.LaunchHook(ctx, ets2Launch())
	if code, text := refused(err); code != "updating" || text != "TruckersMP didn't start because its files are updating. Try again in a few minutes." {
		t.Fatalf("ETS2 updating: %v", err)
	}
	write(t, core, saved)
	os.Chtimes(core, fi.ModTime(), fi.ModTime())
	// The injector could not be copied.
	os.Remove(injectorSource())
	os.Remove(inj)
	writeFlag(flagPath(), games[0], now())
	err = h.LaunchHook(ctx, ets2Launch())
	if code, text := refused(err); code != "launcher-failed" ||
		text != "TruckersMP didn't start because VaporOS couldn't set up its launcher. Restart VaporOS and try again." {
		t.Fatalf("no injector: %v", err)
	}
	write(t, injectorSource(), string(b.injector))

	// The Linux build cannot run the mod: without a flag it starts as
	// ever; with one, the flag goes and the start is refused.
	linux := func() *extensions.Launch {
		return &extensions.Launch{App: 227300, Argv: words(reaper + "|SteamLaunch|AppId=227300|--|" + wrapper + "|--|" +
			steamHome + "/steamapps/common/SteamLinuxRuntime_sniper/_v2-entry-point|--verb=waitforexitandrun|--|" + ets2Dir + "/bin/linux_x64/eurotrucks2")}
	}
	l = linux()
	if err := h.LaunchHook(ctx, l); err != nil || !slices.Equal(l.Argv, linux().Argv) {
		t.Fatalf("the Linux build without a flag: %v %q", err, l.Argv)
	}
	writeFlag(flagPath(), games[0], now())
	l = linux()
	err = h.LaunchHook(ctx, l)
	if code, text := refused(err); code != "linux-ets2" ||
		text != "TruckersMP didn't start because ETS2 isn't set to run with Proton. Restart VaporOS and try again." {
		t.Fatalf("the Linux build with a flag: %v", err)
	}
	if _, err := os.Stat(flagPath()); !os.IsNotExist(err) {
		t.Error("the Linux build's start leaves its flag")
	}
	writeFlag(flagPath(), games[1], now())
	l = &extensions.Launch{App: 270880, Argv: words(atsDir + "/bin/linux_x64/amtrucks|-nointro")}
	if code, text := refused(h.LaunchHook(ctx, l)); code != "linux-ats" || !strings.Contains(text, "because ATS isn't set to run with Proton.") {
		t.Fatalf("ATS's Linux build with a flag: %q %q", code, text)
	}

	// Another app passes untouched.
	l = &extensions.Launch{App: 440, Argv: []string{"/x/hl2.exe"}}
	if err := h.LaunchHook(ctx, l); err != nil || len(l.Argv) != 1 {
		t.Fatalf("another app: %v %q", err, l.Argv)
	}
}

// A changed injector in the image replaces the home's copy.
func TestEnsureInjector(t *testing.T) {
	b := newBox(t)
	dst := filepath.Join(homeDir(), binRel)
	if err := ensureInjector(injectorSource(), dst); err != nil || read(t, dst) != string(b.injector) {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dst)
	time.Sleep(10 * time.Millisecond)
	if err := ensureInjector(injectorSource(), dst); err != nil {
		t.Fatal(err)
	}
	if fi2, _ := os.Stat(dst); !fi2.ModTime().Equal(fi.ModTime()) {
		t.Error("a matching copy was written again")
	}
	write(t, injectorSource(), "MZ new")
	if err := ensureInjector(injectorSource(), dst); err != nil || read(t, dst) != "MZ new" {
		t.Fatal(err)
	}
}

func TestShortcutArgs(t *testing.T) {
	newBox(t)
	h := newHelper()
	for _, c := range []struct{ shortcut, in, want string }{
		// prepare wrote no arguments: the hook adds them.
		{"truckersmp/ets2", reaper + "|SteamLaunch|AppId=3228583970|--|" + wrapper + "|--|/usr/bin/vos",
			reaper + "|SteamLaunch|AppId=3228583970|--|" + wrapper + "|--|/usr/bin/vos|ext|truckersmp|mp|ets2"},
		{"truckersmp/ats", "/usr/bin/vos", "/usr/bin/vos|ext|truckersmp|mp|ats"},
		// prepare wrote them, or someone changed the shortcut: left alone.
		{"truckersmp/ets2", "/usr/bin/vos|ext|truckersmp|mp|ets2", "/usr/bin/vos|ext|truckersmp|mp|ets2"},
		{"truckersmp/ets2", "/usr/bin/vos|ext|truckersmp|mp|ats", "/usr/bin/vos|ext|truckersmp|mp|ats"},
		{"truckersmp/ets2", "/usr/bin/true", "/usr/bin/true"},
		{"truckersmp/other", "/usr/bin/vos", "/usr/bin/vos"},
	} {
		l := &extensions.Launch{Shortcut: c.shortcut, Argv: words(c.in)}
		if err := h.LaunchHook(context.Background(), l); err != nil || !slices.Equal(l.Argv, words(c.want)) {
			t.Errorf("%s %q: %v %q", c.shortcut, c.in, err, l.Argv)
		}
	}
}
