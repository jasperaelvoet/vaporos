package buildcheck

import (
	"reflect"
	"testing"
)

func TestSysctlKeys(t *testing.T) {
	got := sysctlKeys([]byte("# c\n; c\nkernel.demo = 1\n-vm.demo=2\nkernel/sched_demo = 3\nnet/ipv4/conf/enp3s0.200/forwarding = 1\nbroken line\n"))
	want := []string{"kernel.demo", "vm.demo", "kernel.sched_demo", "net.ipv4.conf.enp3s0/200.forwarding"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
}

func TestSysctlOverlapAndNet(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"kernel.sysrq", "kernel.sysrq", true},
		{"kernel.sysrq", "kernel.sysrq_x", false},
		{"kernel.*", "kernel.sysrq", true},
		{"vm.max_map_count", "vm.*", true},
		{"vm.*", "kernel.sysrq", false},
	} {
		if sysctlOverlap(c.a, c.b) != c.want {
			t.Errorf("%s %s: want %v", c.a, c.b, c.want)
		}
	}
	for k, want := range map[string]bool{"net.core.rmem_max": true, "net": true, "*.core.x": true, "kernel.x": false, "network.x": false} {
		if isNetSysctl(k) != want {
			t.Errorf("%s: want %v", k, want)
		}
	}
}

func TestSysctlRules(t *testing.T) {
	base := newBase(t)
	other := t.TempDir()
	writeTree(t, other, map[string]string{"usr/lib/sysctl.d/50-other.conf": "kernel.other_demo = 1\n"})
	for name, c := range map[string]struct {
		conf, want string
	}{
		"own key":            {"kernel.demo = 1\n", ""},
		"base usr/lib":       {"vm.max_map_count = 2\n", "sets vm.max_map_count, which the base sets too (usr/lib/sysctl.d/99-vos.conf)"},
		"base etc, via link": {"fs.inotify.max_user_watches = 1\n", "sets fs.inotify.max_user_watches, which the base sets too (etc/sysctl.d/99-sysctl.conf)"},
		"slash form":         {"kernel/sysrq = 1\n", "sets kernel.sysrq, which the base sets too"},
		"glob":               {"vm.* = 1\n", "sets vm.*, which the base sets too"},
		"other extension":    {"kernel.other_demo = 2\n", "sets kernel.other_demo, which the extension in " + other + " sets too"},
		"network":            {"net.core.rmem_max = 1\n", "sets net.core.rmem_max, a network setting"},
	} {
		t.Run(name, func(t *testing.T) {
			tree, d := newDemo(t)
			d = declare(t, tree, "service", "sysctl")
			writeTree(t, tree, map[string]string{"usr/lib/sysctl.d/50-demo.conf": c.conf})
			r := run(t, tree, base, d, other)
			if c.want == "" {
				wantClean(t, r)
			} else {
				wantProblem(t, r, "usr/lib/sysctl.d/50-demo.conf: "+c.want)
			}
		})
	}
}
