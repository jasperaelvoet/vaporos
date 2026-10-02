package buildcheck

import (
	"strings"
	"testing"
)

func TestTmpfilesLines(t *testing.T) {
	own := ownAreas("demo")
	for _, line := range []string{
		"",
		"   ",
		"# w /proc/sys/kernel/sysrq - - - - 1",
		"d /var/lib/vos/ext/data/demo 0750 demo demo -",
		"d %S/vos/ext/data/demo/cache 0750 - - -",
		"d /var/home/vapor/.local/share/vaporos/ext/demo 0700 vapor vapor",
		"d /run/demo 1777 - - -",
		"f /run/demo/flag 0644 - - - hello %% world",
		"f^ /run/demo/key 0600 - - - demo.key",
		"D %t/demo/sockets",
		"r! /run/demo/*.lock",
		"Z /var/lib/vos/ext/data/demo ~0750 demo demo",
		"z '/run/demo/a b' 0640 demo demo",
		"e /var/cache/demo - - - 30d",
		"L+ /var/log/demo/latest - - - - /var/log/demo/2026.log",
		"L /var/cache/demo/link - - - - ../demo/x",
		"L /var/lib/vos/ext/data/demo/config.toml - - - - /usr/share/demo/config.toml",
		"C /var/home/vapor/.local/share/vaporos/ext/demo/config - - - - /usr/share/demo/config",
		"C /run/demo/x",
		"a+ /var/lib/vos/ext/data/demo - - - - u:vapor:rx",
	} {
		if p := tmpfilesProblem(line, own); p != "" {
			t.Errorf("%q: %s", line, p)
		}
	}
	for line, want := range map[string]string{
		"w /proc/sys/kernel/sysrq - - - - 1":           "/proc/sys/kernel/sysrq is outside the extension's own paths",
		"w- /sys/module/amdgpu/parameters/x - - - - 1": "is outside",
		"f /etc/demo.conf":                             "is outside",
		"d /var/lib/vos/ext/data/other":                "is outside",
		"d /var/lib/vos/ext/data/demo2":                "is outside",
		"r /var/lib/vos/config.json":                   "is outside",
		"x /var/lib/vos/*":                             "is outside",
		"d /root/demo":                                 "is outside",
		"d /home/vapor/demo":                           "is outside",
		"d /var/home/vapor/.config/demo":               "is outside",
		"d /run/demo/../vos":                           "is outside",
		"d /run/demo*":                                 "is outside",
		"d /tmp/demo":                                  "is outside",
		"d %T/demo":                                    "is outside",
		"d %h/demo":                                    "specifiers",
		"d relative/demo":                              "not an absolute path",
		"c /run/demo/mem 0666 - - - 1:1":               "type c creates a device node",
		"b+ /run/demo/sda 0600 - - - 8:0":              "type b+ creates a device node",
		"t /var/lib/vos/ext/data/demo/bin - - - - security.capability=0sAQAAAg==": "extended attributes",
		"T /run/demo - - - - user.x=1":                                            "extended attributes",
		"L /run/demo/shadow - - - - /etc/shadow":                                  "its target /etc/shadow is outside",
		"L /run/demo/up - - - - ../../etc/x":                                      "its target ../../etc/x is outside",
		"L /run/demo/spec - - - - %h/x":                                           "its target %h/x is outside",
		"C /run/demo/shadow - - - - /etc/shadow":                                  "its source /etc/shadow is outside",
		"C /run/demo/rel - - - - relative":                                        "its source relative is outside",
		"f /run/demo/su 4755 root root - x":                                       "mode 4755 sets the setuid or setgid bit",
		"z /var/home/vapor/.local/share/vaporos/ext/demo/x 2755 vapor vapor":      "mode 2755 sets the setuid",
		"Z /run/demo ~:6755":                                                      "sets the setuid",
		"d /run/demo zz":                                                          `mode "zz" is not octal`,
		"y /run/demo":                                                             `unknown type "y"`,
		"d? /run/demo":                                                            `unknown type "d?"`,
		`d "/run/demo`:                                                            "unbalanced quotes",
		`d /run/de\x6do`:                                                          "backslash escape",
		"d":                                                                       "not a type and a path",
	} {
		if p := tmpfilesProblem(line, own); !strings.Contains(p, want) {
			t.Errorf("%q: %q, want %q", line, p, want)
		}
	}
}

func TestTmpfilesInTree(t *testing.T) {
	tree, _ := newDemo(t)
	d := declare(t, tree, "service", "tmpfiles")
	writeTree(t, tree, map[string]string{
		"usr/lib/tmpfiles.d/demo.conf":   "d /var/lib/vos/ext/data/demo 0750 demo demo -\nw /proc/sys/vm/swappiness - - - - 10\n",
		"usr/lib/tmpfiles.d/README":      "w /proc/sys/kernel/sysrq - - - - 1\n",
		"usr/lib/tmpfiles.d/linked.conf": "@/etc/hostname",
	})
	r := run(t, tree, newBase(t), d)
	wantProblem(t, r, "usr/lib/tmpfiles.d/demo.conf:2: /proc/sys/vm/swappiness is outside")
	wantProblem(t, r, "usr/lib/tmpfiles.d/linked.conf: ")
	if len(r.Problems) != 2 {
		t.Fatalf("problems %q", r.Problems)
	}
}
