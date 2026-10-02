package buildcheck

import (
	"strings"
	"testing"
)

// tmpfilesProblem checks one line alone, on a box with nothing in /usr.
func tmpfilesProblem(text string) string {
	own := ownAreas("demo")
	l, p := parseTmpfiles(text, own)
	if p != "" || l.typ == "" {
		return p
	}
	return newBoxView(nil, own, []tmpfilesLine{l}).lineProblem(l)
}

func TestOwnAreas(t *testing.T) {
	got := strings.Join(ownAreas("demo"), " ")
	want := "/var/lib/vos/ext/data/demo /var/home/vapor/.local/share/vaporos/ext/demo /run/vos-ext/demo"
	if got != want {
		t.Fatalf("%s, want %s", got, want)
	}
}

func TestTmpfilesLines(t *testing.T) {
	for _, line := range []string{
		"",
		"   ",
		"# w /proc/sys/kernel/sysrq - - - - 1",
		"d /var/lib/vos/ext/data/demo 0750 demo demo -",
		"d %S/vos/ext/data/demo/cache 0750 - - -",
		"d /var/home/vapor/.local/share/vaporos/ext/demo 0700 vapor vapor",
		"d /run/vos-ext/demo 1777 - - -",
		"f /run/vos-ext/demo/flag 0644 - - - hello %% world",
		"f^ /run/vos-ext/demo/key 0600 - - - demo.key",
		"D %t/vos-ext/demo/sockets",
		"r! /run/vos-ext/demo/*.lock",
		"Z /var/lib/vos/ext/data/demo ~0750 demo demo",
		"z '/run/vos-ext/demo/a b' 0640 demo demo",
		"e /run/vos-ext/demo - - - 30d",
		"L+ /run/vos-ext/demo/latest - - - - /var/lib/vos/ext/data/demo/2026.log",
		"L /run/vos-ext/demo/sub/link - - - - ../x",
		"L /var/lib/vos/ext/data/demo/config.toml - - - - /usr/share/demo/config.toml",
		"L /run/vos-ext/demo/factory",
		"C /var/home/vapor/.local/share/vaporos/ext/demo/config - - - - /usr/share/demo/config",
		"C /run/vos-ext/demo/x",
		"a+ /var/lib/vos/ext/data/demo - - - - u:vapor:rx",
		`a+ /var/lib/vos/ext/data/demo - - - - u:vapor:rx\x`,
	} {
		if p := tmpfilesProblem(line); p != "" {
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
		"d /run/vos-ext/demo/../vos":                   "is outside",
		"d /run/vos-ext/demo*":                         "is outside",
		"d /run/vos-ext":                               "is outside",
		"d /run/demo":                                  "is outside",
		"d /var/cache/demo":                            "is outside",
		"d %C/demo":                                    "is outside",
		"d /var/log/demo":                              "is outside",
		"d /tmp/demo":                                  "is outside",
		"d %T/demo":                                    "is outside",
		"d %h/demo":                                    "specifiers",
		"d relative/demo":                              "not an absolute path",
		"c /run/vos-ext/demo/mem 0666 - - - 1:1":       "type c creates a device node",
		"b+ /run/vos-ext/demo/sda 0600 - - - 8:0":      "type b+ creates a device node",
		"t /var/lib/vos/ext/data/demo/bin - - - - security.capability=0sAQAAAg==": "extended attributes",
		"T /run/vos-ext/demo - - - - user.x=1":                                    "extended attributes",
		"L /run/vos-ext/demo/shadow - - - - /etc/shadow":                          "its target /etc/shadow is outside",
		"L /run/vos-ext/demo/up - - - - ../../etc/x":                              "its target ../../etc/x is outside",
		"L /run/vos-ext/demo/spec - - - - %h/x":                                   "its target %h/x is outside",
		"L /run/vos-ext/demo/run - - - - /run/vos-ext/other":                      "its target /run/vos-ext/other is outside",
		`L+ /run/vos-ext/demo/x - - - - \x2fetc\x2fshadow`:                        "a backslash in its argument",
		`C /run/vos-ext/demo/x - - - - /usr/share/demo/\x2e\x2e/\x2e\x2e/etc`:     "a backslash in its argument",
		`w /run/vos-ext/demo/x - - - - a\nb`:                                      "a backslash in its argument",
		`f /run/vos-ext/demo/x - - - - a\nb`:                                      "a backslash in its argument",
		"L~ /run/vos-ext/demo/x - - - - L2V0Yy9zaGFkb3c=":                         "the ~ and ^ modifiers",
		"C^ /run/vos-ext/demo/x - - - - shadow":                                   "the ~ and ^ modifiers",
		"C /run/vos-ext/demo/shadow - - - - /etc/shadow":                          "its source /etc/shadow is outside",
		"C /run/vos-ext/demo/rel - - - - relative":                                "its source relative is outside",
		"f /run/vos-ext/demo/su 4755 root root - x":                               "mode 4755 sets the setuid or setgid bit",
		"z /var/home/vapor/.local/share/vaporos/ext/demo/x 2755 vapor vapor":      "mode 2755 sets the setuid",
		"Z /run/vos-ext/demo ~:6755":                                              "sets the setuid",
		"d /run/vos-ext/demo zz":                                                  `mode "zz" is not octal`,
		"y /run/vos-ext/demo":                                                     `unknown type "y"`,
		"d? /run/vos-ext/demo":                                                    `unknown type "d?"`,
		`d "/run/vos-ext/demo`:                                                    "unbalanced quotes",
		`d /run/vos-ext/de\x6do`:                                                  "backslash escape",
		"d":                                                                       "not a type and a path",
	} {
		if p := tmpfilesProblem(line); !strings.Contains(p, want) {
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

// TestTmpfilesThroughSymlinks resolves lines as the box does: through the
// symlinks of /usr's layers and those the extension's own L lines make.
func TestTmpfilesThroughSymlinks(t *testing.T) {
	const conf = "usr/lib/tmpfiles.d/demo.conf"
	for name, c := range map[string]struct {
		tree, base, other map[string]string
		lines             string
		want              []string // problems, in order; none for a clean tree
	}{
		"a link into /usr": {
			tree:  map[string]string{"usr/share/demo/config/a": "a"},
			lines: "L /run/vos-ext/demo/config - - - - /usr/share/demo/config\nf /run/vos-ext/demo/config/b\n",
		},
		"an image link out of /usr as the target": {
			tree: map[string]string{"usr/share/demo/etc": "@/etc"},
			lines: "L /run/vos-ext/demo/e - - - - /usr/share/demo/etc\n" +
				"f /run/vos-ext/demo/e/shadow 0644 - - - x\n",
			want: []string{
				conf + ":1: /run/vos-ext/demo/e: its target /usr/share/demo/etc leads to /etc, outside",
				conf + ":2: /run/vos-ext/demo/e/shadow leads to /etc/shadow through a symlink, outside",
			},
		},
		"written through a link to a /usr directory": {
			tree: map[string]string{"usr/share/demo/etc": "@../../../etc"},
			lines: "L /run/vos-ext/demo/d - - - - /usr/share/demo\n" +
				"w /run/vos-ext/demo/d/etc/shadow - - - - x\n",
			want: []string{conf + ":2: /run/vos-ext/demo/d/etc/shadow leads to /etc/shadow through a symlink"},
		},
		"up from where a link led": {
			tree: map[string]string{"usr/share/demo/dir/a": "a", "usr/share/demo/etc": "@/etc"},
			lines: "L /run/vos-ext/demo/sub/d - - - - /usr/share/demo/dir\n" +
				"f /run/vos-ext/demo/sub/d/../etc/shadow\n",
			want: []string{conf + ":2: /run/vos-ext/demo/sub/d/../etc/shadow leads to /etc/shadow through a symlink"},
		},
		"a relative target through /usr": {
			tree:  map[string]string{"usr/share/demo/etc": "@/etc"},
			lines: "L /run/vos-ext/demo/e - - - - ../../../usr/share/demo/etc/shadow\n",
			want:  []string{conf + ":1: /run/vos-ext/demo/e: its target ../../../usr/share/demo/etc/shadow leads to /etc/shadow"},
		},
		"a link of the base": {
			base:  map[string]string{"usr/share/factory/run/vos-ext/demo/x": "@/etc/shadow"},
			lines: "C /run/vos-ext/demo/x\n",
			want:  []string{conf + ":1: /run/vos-ext/demo/x: its source /usr/share/factory/run/vos-ext/demo/x leads to /etc/shadow"},
		},
		"a link of another extension": {
			other: map[string]string{"usr/share/shared/etc": "@/etc"},
			lines: "C /run/vos-ext/demo/shadow - - - - /usr/share/shared/etc/shadow\n",
			want:  []string{conf + ":1: /run/vos-ext/demo/shadow: its source /usr/share/shared/etc/shadow leads to /etc/shadow"},
		},
		"a chain of its own links": {
			lines: "L /run/vos-ext/demo/a - - - - /run/vos-ext/demo/b\nL /run/vos-ext/demo/b - - - - /run/vos-ext/demo\n" +
				"f /run/vos-ext/demo/a/x\nf /run/vos-ext/demo/a/../x\n",
			want: []string{conf + ":4: /run/vos-ext/demo/a/../x leads to /run/vos-ext/x through a symlink"},
		},
		"a link made through a link": {
			lines: "L /run/vos-ext/demo/a - - - - /run/vos-ext/demo/b\nL /run/vos-ext/demo/a/c - - - - /usr/share/demo\n",
			want:  []string{conf + ":2: /run/vos-ext/demo/a/c: an L line's link is made through a symlink, at /run/vos-ext/demo/b/c"},
		},
		"a glob over its links": {
			tree: map[string]string{"usr/share/demo/etc": "@/etc"},
			lines: "L /run/vos-ext/demo/d - - - - /usr/share/demo\n" +
				"w /run/vos-ext/demo/*/etc/shadow - - - - x\n" +
				"z /run/vos-ext/demo/*.lock 0644\n",
			want: []string{conf + ":2: /run/vos-ext/demo/*/etc/shadow leads to /etc/shadow through a symlink"},
		},
		"a glob in /usr": {
			lines: "L /run/vos-ext/demo/d - - - - /usr/share/demo\nr /run/vos-ext/demo/d/*\n",
			want:  []string{conf + ":2: /run/vos-ext/demo/d/*: a glob in /usr/share/demo"},
		},
		"a loop": {
			lines: "L /run/vos-ext/demo/a - - - - b\nL /run/vos-ext/demo/b - - - - a\nf /run/vos-ext/demo/a/x\n",
			want: []string{
				conf + ":1: /run/vos-ext/demo/a: its target b: too many levels",
				conf + ":2: /run/vos-ext/demo/b: its target a: too many levels",
				conf + ":3: /run/vos-ext/demo/a/x: too many levels",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			tree, _ := newDemo(t)
			d := declare(t, tree, "service", "tmpfiles")
			base, other := newBase(t), t.TempDir()
			writeTree(t, tree, c.tree)
			writeTree(t, base, c.base)
			writeTree(t, other, c.other)
			writeTree(t, tree, map[string]string{conf: c.lines})
			r := run(t, tree, base, d, other)
			if len(r.Problems) != len(c.want) {
				t.Fatalf("problems %q, want %q", r.Problems, c.want)
			}
			for i, w := range c.want {
				if !strings.HasPrefix(r.Problems[i], w) {
					t.Errorf("problem %q, want %q", r.Problems[i], w)
				}
			}
		})
	}
}
