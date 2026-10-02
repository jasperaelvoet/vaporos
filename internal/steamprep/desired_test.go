package steamprep

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

func TestParseDesired(t *testing.T) {
	art := filepath.Join(config.ExtMountedLibDir, "star-citizen", "art")
	data := `{"set":"4","dispatcher":true,"default_compat_tool":"proton-cachyos-slr",
	"apps":[
		{"app":227300,"compat_tool":"proton-cachyos-slr","hooks":["truckersmp","Bad Hook"],"beta":{"branch":"temporary_1_61","request":"7"}},
		{"app":227300,"compat_tool":"twice","hooks":["twice-hook"]},
		{"app":0},
		{"app":270880,"compat_tool":"../../etc"},
		{"app":1,"beta":{"branch":"x\"y","request":"8"}},
		{"app":2,"beta":{"branch":"","request":"9"}},
		{"app":3,"beta":null},
		{"app":4,"beta":"temporary_1_61"},
		{"app":5,"beta":{"branch":"b"}}
	],
	"shortcuts":[
		{"owner":"star-citizen","key":"launcher","name":"Star Citizen","exe":"/var/mnt/g/setup.exe","start_dir":"/var/mnt/g","compat_tool":"","art":"` + art + `"},
		{"owner":"star-citizen","key":"launcher","name":"again","exe":"/a","start_dir":"/"},
		{"owner":"x","key":"y","name":"relative","exe":"setup.exe","start_dir":"/"},
		{"owner":"x","key":"z","name":"quote","exe":"/a\"b","start_dir":"/"},
		{"owner":"x","key":"w","name":"art elsewhere","exe":"/a","start_dir":"/","art":"/etc"},
		{"owner":"x","key":"v","name":"other's art","exe":"/a","start_dir":"/","art":"` + art + `"},
		{"owner":"X","key":"v","name":"bad id","exe":"/a","start_dir":"/"}
	],
	"release":[{"app":0},{"app":270880}]}`
	var logs []string
	d, err := parseDesired([]byte(data), func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	if err != nil {
		t.Fatal(err)
	}
	// A branch request that is not well formed goes alone, and the app's
	// branch is then left as it is.
	if len(d.Apps) != 6 || d.Apps[0].App != 227300 || d.Apps[1].App != 1 || d.Apps[2].App != 2 || d.Apps[3].App != 3 ||
		strings.Join(d.Apps[0].Hooks, ",") != "truckersmp" || *d.Apps[0].Beta != (BetaWant{Branch: "temporary_1_61", Request: "7"}) ||
		*d.Apps[2].Beta != (BetaWant{Request: "9"}) || d.Apps[3].Beta != nil || d.Apps[3].keepBranch {
		t.Errorf("apps %+v", d.Apps)
	}
	for _, a := range []AppWant{d.Apps[1], d.Apps[4], d.Apps[5]} {
		if a.Beta != nil || !a.keepBranch {
			t.Errorf("app %d: %+v", a.App, a)
		}
	}
	// Every extension named counts, in entries left out too.
	for _, id := range []string{"truckersmp", "twice-hook", "star-citizen", "x"} {
		if !d.names(id) {
			t.Errorf("%s not named", id)
		}
	}
	if d.names("X") || d.names("Bad Hook") || d.names("nobody") {
		t.Error("names what is not an extension id")
	}
	if len(d.Shortcuts) != 1 || d.Shortcuts[0].Name != "Star Citizen" {
		t.Errorf("shortcuts %+v", d.Shortcuts)
	}
	if r := d.releases(); len(r) != 1 || !r[270880] {
		t.Errorf("release %v", r)
	}
	if len(logs) != 12 {
		t.Errorf("%d log lines: %q", len(logs), logs)
	}
	if _, err := parseDesired([]byte(`{"set":`), func(string, ...any) {}); err == nil {
		t.Error("broken steam.json accepted")
	}
	d, _ = parseDesired([]byte(`{"default_compat_tool":"a b"}`), func(string, ...any) {})
	if d.DefaultCompatTool != "" {
		t.Errorf("default %q", d.DefaultCompatTool)
	}
}
