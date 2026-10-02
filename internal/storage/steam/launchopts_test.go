package steam

import "testing"

func TestWrapLaunchOptions(t *testing.T) {
	const d = "/usr/bin/vos ext launch --app 227300 "
	const n = "/usr/bin/vos ext launch --app 1091500"
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"", d + "%command%", true},
		{"  ", d + "%command%", true},
		{"-nointro -64bit", d + "%command% -nointro -64bit", true},
		{"PROTON_LOG=1 %command%", "PROTON_LOG=1 " + d + "%command%", true},
		{"env -- A=1 %command% -x", "env -- A=1 " + d + "%command% -x", true},
		{"gamemoderun %command%", "gamemoderun " + d + "%command%", true},
		{"echo x; %command%", "echo x; " + d + "%command%", true},
		{"%command%", d + "%command%", true},
		{"x%command%", "x" + d + "%command%", true},
		// Already wrapped, for this app or another: the token is replaced.
		{d + "%command% -nointro", d + "%command% -nointro", true},
		{"PROTON_LOG=1 " + d + "%command%", "PROTON_LOG=1 " + d + "%command%", true},
		{"gamemoderun " + n + " %command% -x", "gamemoderun " + d + "%command% -x", true},
		{"/usr/bin/vos ext launch --app 1 /usr/bin/vos ext launch --app 2 %command%", d + "%command%", true},
		// Tokens anywhere, followed by any run of spaces and tabs.
		{d + "mangohud %command%", "mangohud " + d + "%command%", true},
		{n + " mangohud %command%", "mangohud " + d + "%command%", true},
		{d + " %command%", d + "%command%", true},
		{n + "\t%command%", d + "%command%", true},
		{n + " \t %command% -x", d + "%command% -x", true},
		{n + "%command%", d + "%command%", true},
		{"%command% -x " + n, d + "%command% -x ", true},
		{n + " -nointro", d + "%command% -nointro", true},
		// Not a token of ours: kept in front of it.
		{"/usr/bin/vos ext launch --app x %command%", "/usr/bin/vos ext launch --app x " + d + "%command%", true},
		{"/usr/bin/vos ext launch --app 7x %command%", "/usr/bin/vos ext launch --app 7x " + d + "%command%", true},
		{"vos ext launch --app 7 %command%", "vos ext launch --app 7 " + d + "%command%", true},
		// Several %command%: left without our tokens, and reported.
		{"%command% ; %command%", "%command% ; %command%", false},
		{"mangohud %command% && echo %command%", "mangohud %command% && echo %command%", false},
		{n + " %command% ; " + d + "%command%", "%command% ; %command%", false},
	} {
		got, ok := WrapLaunchOptions(c.in, 227300)
		if got != c.want || ok != c.ok {
			t.Errorf("Wrap(%q) = %q %v, want %q %v", c.in, got, ok, c.want, c.ok)
		}
		again, _ := WrapLaunchOptions(got, 227300)
		if again != got {
			t.Errorf("Wrap(%q) is not idempotent: %q", got, again)
		}
	}
	if got, ok := WrapLaunchOptions("-x", 0); ok || got != "-x" {
		t.Errorf("app 0: %q %v", got, ok)
	}
}

func TestUnwrapLaunchOptions(t *testing.T) {
	for _, in := range []string{
		"PROTON_LOG=1 %command%", "env -- A=1 %command% -x", "gamemoderun %command%",
		"echo x; %command%", "%command% -nointro", "%command%",
	} {
		w, _ := WrapLaunchOptions(in, 227300)
		if got := UnwrapLaunchOptions(w); got != in {
			t.Errorf("Unwrap(Wrap(%q)) = %q", in, got)
		}
	}
	for in, want := range map[string]string{
		"":         "",
		"-nointro": "-nointro",
		"/usr/bin/vos ext launch --app 1 %command% -nointro":                                     "%command% -nointro",
		"/usr/bin/vos ext launch --app 1 mangohud %command%":                                     "mangohud %command%",
		"/usr/bin/vos ext launch --app 1  %command%":                                             "%command%",
		"/usr/bin/vos ext launch --app 1\t%command%":                                             "%command%",
		"%command% ; /usr/bin/vos ext launch --app 1 %command%":                                  "%command% ; %command%",
		"/usr/bin/vos ext launch --app 2 %command% && /usr/bin/vos ext launch --app 3 %command%": "%command% && %command%",
		"/usr/bin/vos ext launch --app 1":                                                        "",
		"/usr/bin/vos ext launch --shortcut a/b %command%":                                       "/usr/bin/vos ext launch --shortcut a/b %command%",
		"/usr/bin/vos ext launch --app %command%":                                                "/usr/bin/vos ext launch --app %command%",
	} {
		if got := UnwrapLaunchOptions(in); got != want {
			t.Errorf("Unwrap(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortcutLaunchOptions(t *testing.T) {
	opts := ShortcutLaunchOptions("star-citizen", "launcher")
	if opts != "/usr/bin/vos ext launch --shortcut star-citizen/launcher %command%" {
		t.Errorf("got %q", opts)
	}
	for in, want := range map[string]string{
		opts:                           "star-citizen/launcher",
		"MANGOHUD=1 " + opts + " -fps": "star-citizen/launcher",
		"/usr/bin/vos ext launch --shortcut truckersmp/ets2":   "truckersmp/ets2",
		"/usr/bin/vos ext launch --app 1 %command%":            "",
		"/usr/bin/vos ext launch --shortcut Bad/key %command%": "",
		"/usr/bin/vos ext launch --shortcut nokey %command%":   "",
		"/usr/bin/vos ext launch --shortcut a/b/c %command%":   "",
		"": "",
	} {
		owner, key, ok := ShortcutRef(in)
		got := ""
		if ok {
			got = owner + "/" + key
		}
		if got != want {
			t.Errorf("ShortcutRef(%q) = %q, want %q", in, got, want)
		}
	}
}
