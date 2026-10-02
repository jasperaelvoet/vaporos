package buildcheck

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestUnitRules(t *testing.T) {
	base := newBase(t)
	const unit = "usr/lib/systemd/system/demo.service"
	const dropIn = "usr/lib/systemd/system/demo.service.d/vos.conf"
	for name, c := range map[string]struct {
		files  map[string]string
		remove []string
		want   string // a problem, or "" for none
	}{
		"no drop-in":            {remove: []string{dropIn}, want: "demo.service: needs a drop-in (demo.service.d/*.conf)"},
		"drop-in without limit": {files: map[string]string{dropIn: "[Service]\nNice=5\n"}, want: "needs a drop-in"},
		"only the unit limits":  {files: map[string]string{unit: "[Service]\nExecStart=/usr/bin/demo\nTimeoutStartSec=10\n", dropIn: "[Service]\nNice=5\n"}, want: "needs a drop-in"},
		"infinity":              {files: map[string]string{dropIn: "[Service]\nTimeoutStartSec=infinity\n"}, want: "does not bound the start"},
		"zero":                  {files: map[string]string{dropIn: "[Service]\nTimeoutStartSec=0\n"}, want: "does not bound the start"},
		"reset":                 {files: map[string]string{dropIn: "[Service]\nTimeoutStartSec=30\nTimeoutStartSec=\n"}, want: "is not a time span"},
		"garbage":               {files: map[string]string{dropIn: "[Service]\nTimeoutStartSec=soon\n"}, want: "is not a time span"},
		"later drop-in unbounds": {files: map[string]string{
			"usr/lib/systemd/system/demo.service.d/zz.conf": "[Service]\nTimeoutSec=infinity\n",
		}, want: "does not bound the start"},
		"TimeoutSec":           {files: map[string]string{dropIn: "[Service]\nTimeoutSec=1min 30s\n"}},
		"Before a base target": {files: map[string]string{unit: "[Unit]\nBefore=multi-user.target\n[Service]\nUser=demo\n"}, want: unit + ": Before=multi-user.target"},
		"Before in a drop-in":  {files: map[string]string{dropIn: "[Unit]\nBefore=vosd.service\n[Service]\nTimeoutStartSec=30\n"}, want: dropIn + ": Before=vosd.service"},
		"Before a base instance": {files: map[string]string{
			unit: "[Unit]\nBefore=demo-helper.service getty@tty1.service\n[Service]\nUser=demo\n",
		}, want: "Before=getty@tty1.service"},
		"Before an /etc unit": {files: map[string]string{unit: "[Unit]\nBefore=local.service\n[Service]\nUser=demo\n"}, want: "Before=local.service"},
		"Before its own unit": {files: map[string]string{
			unit: "[Unit]\nBefore=demo-helper.service\n[Service]\nUser=demo\n",
			"usr/lib/systemd/system/demo-helper.service":            "[Service]\nUser=demo\n",
			"usr/lib/systemd/system/demo-helper.service.d/vos.conf": "[Service]\nTimeoutStartSec=5\n",
		}},
		"drop-in for a base unit": {files: map[string]string{
			"usr/lib/systemd/system/vosd.service.d/x.conf": "[Service]\nTimeoutStartSec=5\n",
		}, want: "usr/lib/systemd/system/vosd.service.d: drop-in for vosd.service, which the extension does not ship"},
		"drop-in for every service": {files: map[string]string{
			"usr/lib/systemd/system/service.d/x.conf": "[Service]\nTimeoutStartSec=5\n",
		}, want: "usr/lib/systemd/system/service.d: not a unit's drop-in"},
		"wants for a base unit": {files: map[string]string{
			"usr/lib/systemd/system/multi-user.target.wants/demo.service": "@../demo.service",
		}, want: "adds dependencies to multi-user.target"},
		"wants of its own unit": {files: map[string]string{
			"usr/lib/systemd/system/demo.service.wants/vosd.service": "@/usr/lib/systemd/system/vosd.service",
		}},
		"alias": {files: map[string]string{"usr/lib/systemd/system/demo-alias.service": "@demo.service"}},
		"mask":  {files: map[string]string{"usr/lib/systemd/system/demo-mask.service": "@/dev/null"}, want: "masks a unit"},
		"alias elsewhere": {files: map[string]string{
			"usr/lib/systemd/system/demo-alias.service": "@/usr/lib/demo/demo.service",
		}, want: "an alias must point at one of the extension's own units"},
		"template drop-in covers instance": {files: map[string]string{
			"usr/lib/systemd/system/demo@.service":            "[Service]\nUser=demo\n",
			"usr/lib/systemd/system/demo@.service.d/vos.conf": "[Service]\nTimeoutStartSec=5\n",
			"usr/lib/systemd/system/demo@x.service.d/x.conf":  "[Service]\nNice=1\n",
		}},
		"user drop-in for a base unit": {files: map[string]string{
			"usr/lib/systemd/user/vos-gamescope.service.d/x.conf": "[Service]\nEnvironment=A=1\n",
		}, want: "drop-in for vos-gamescope.service"},
		"listed service missing": {remove: []string{unit, dropIn}, want: "services[0]: demo.service is not a unit in usr/lib/systemd/system"},
	} {
		t.Run(name, func(t *testing.T) {
			tree, d := newDemo(t)
			for _, p := range c.remove {
				if err := os.Remove(filepath.Join(tree, filepath.FromSlash(p))); err != nil {
					t.Fatal(err)
				}
			}
			writeTree(t, tree, c.files)
			if _, ok := c.files["usr/lib/systemd/user/vos-gamescope.service.d/x.conf"]; ok {
				d = declare(t, tree, "service", "user-service")
			}
			r := run(t, tree, base, d)
			if c.want == "" {
				wantClean(t, r)
			} else {
				wantProblem(t, r, c.want)
			}
		})
	}
}

func TestRunsAsRoot(t *testing.T) {
	base := newBase(t)
	const unit = "usr/lib/systemd/system/demo.service"
	for name, c := range map[string]struct {
		files map[string]string
		root  bool
	}{
		"User":            {map[string]string{}, false},
		"no User":         {map[string]string{unit: "[Service]\nExecStart=/usr/bin/demo\n"}, true},
		"User=root":       {map[string]string{unit: "[Service]\nUser=root\n"}, true},
		"DynamicUser":     {map[string]string{unit: "[Service]\nDynamicUser=yes\n"}, false},
		"drop-in resets":  {map[string]string{"usr/lib/systemd/system/demo.service.d/zz.conf": "[Service]\nUser=\n"}, true},
		"drop-in sets":    {map[string]string{unit: "[Service]\n", "usr/lib/systemd/system/demo.service.d/zz.conf": "[Service]\nUser=demo\n"}, false},
		"User in [Unit]":  {map[string]string{unit: "[Unit]\nUser=demo\n[Service]\n"}, true},
		"a root template": {map[string]string{"usr/lib/systemd/system/demo@.service": "[Service]\n", "usr/lib/systemd/system/demo@.service.d/vos.conf": "[Service]\nTimeoutStartSec=5\n"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			tree, d := newDemo(t)
			writeTree(t, tree, c.files)
			r := run(t, tree, base, d)
			wantClean(t, r)
			if r.RunsAsRoot != c.root {
				t.Fatalf("runs_as_root %v, want %v", r.RunsAsRoot, c.root)
			}
		})
	}
}

func TestParseTimespan(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"90":        90 * time.Second,
		"90s":       90 * time.Second,
		"1min 30s":  90 * time.Second,
		"1min30s":   90 * time.Second,
		"5m":        5 * time.Minute,
		"1.5h":      90 * time.Minute,
		"500ms":     500 * time.Millisecond,
		" 2 hours ": 2 * time.Hour,
		"0":         0,
	} {
		d, inf, ok := parseTimespan(in)
		if !ok || inf || d != want {
			t.Errorf("%q: %v %v %v, want %v", in, d, inf, ok, want)
		}
	}
	if _, inf, ok := parseTimespan("infinity"); !ok || !inf {
		t.Error("infinity")
	}
	for _, in := range []string{"", "soon", "5 parsecs", "s", "1..2s"} {
		if _, _, ok := parseTimespan(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestParseUnit(t *testing.T) {
	got := parseUnit("u", []byte("# comment\n[Unit]\nDescription=A \\\n  long \\\n# skipped\n  name\n; also a comment\n[Service]\n ExecStart = /bin/x\nnot an assignment\nUser=\n\\"))
	want := []assignment{
		{"u", "Unit", "Description", "A  long  name"},
		{"u", "Service", "ExecStart", "/bin/x"},
		{"u", "Service", "User", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q\nwant %q", got, want)
	}
}

func TestUnitTemplate(t *testing.T) {
	for in, want := range map[string]string{
		"getty@tty1.service": "getty@.service",
		"getty@.service":     "",
		"a@b.c@d.service":    "a@.service",
		"plain.service":      "",
	} {
		got, ok := unitTemplate(in)
		if (want == "") == ok || got != want {
			t.Errorf("%s: %q %v, want %q", in, got, ok, want)
		}
	}
}
