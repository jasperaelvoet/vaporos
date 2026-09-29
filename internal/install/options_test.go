package install

import (
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/system"
)

func TestNormalize(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Options
		want Options
		err  string
	}{
		{"defaults", Options{Disk: " sda "},
			Options{Disk: "sda", Mode: ModeErase, Hostname: "vapor", Libraries: []string{}}, ""},
		{"canonical", Options{Disk: "sda", Mode: "Repair", Hostname: " Living-Room ", Timezone: " Europe/Brussels ",
			Libraries: []string{"a-1", " a-1", "", "B-2"}, Source: " http://builder:8000/ "},
			Options{Disk: "sda", Mode: ModeRepair, Hostname: "living-room", Timezone: "Europe/Brussels",
				Libraries: []string{"a-1", "B-2"}, Source: "http://builder:8000/"}, ""},
		{"repair keeps hostname", Options{Disk: "sda", Mode: "repair"},
			Options{Disk: "sda", Mode: ModeRepair, Libraries: []string{}}, ""},
		{"no disk", Options{}, Options{}, "no disk"},
		{"bad mode", Options{Disk: "sda", Mode: "wipe"}, Options{}, "mode must be"},
		{"dotted hostname", Options{Disk: "sda", Hostname: "vapor.local"}, Options{}, "invalid hostname"},
		{"hyphen hostname", Options{Disk: "sda", Hostname: "-vapor"}, Options{}, "invalid hostname"},
		{"long hostname", Options{Disk: "sda", Hostname: strings.Repeat("a", 64)}, Options{}, "invalid hostname"},
		{"localhost", Options{Disk: "sda", Hostname: " LocalHost "}, Options{}, `"localhost" is reserved`},
		{"repair to localhost", Options{Disk: "sda", Mode: "repair", Hostname: "localhost"}, Options{}, "reserved"},
		{"short password", Options{Disk: "sda", Password: "vapor"}, Options{}, "at least 8"},
		{"nul password", Options{Disk: "sda", Password: "12345678\x00"}, Options{}, "not valid"},
		{"traversal timezone", Options{Disk: "sda", Timezone: "../../etc/shadow"}, Options{}, "invalid timezone"},
		{"bad uuid", Options{Disk: "sda", Libraries: []string{"x/../y"}}, Options{}, "invalid filesystem UUID"},
		{"oci source", Options{Disk: "sda", Source: " oci://ghcr.io/jasperaelvoet/vaporos ", Channel: " dev-tooling "},
			Options{Disk: "sda", Mode: ModeErase, Hostname: "vapor", Libraries: []string{},
				Source: "oci://ghcr.io/jasperaelvoet/vaporos", Channel: "dev-tooling"}, ""},
		{"live alias", Options{Disk: "sda", Source: "live"},
			Options{Disk: "sda", Mode: ModeErase, Hostname: "vapor", Libraries: []string{}}, ""},
		{"bad oci source", Options{Disk: "sda", Source: "oci://ghcr.io/Jasper/VaporOS"}, Options{}, "invalid repository"},
		{"bad channel", Options{Disk: "sda", Source: "oci://ghcr.io/x/y", Channel: "no/slashes"}, Options{}, "invalid channel"},
		{"relative source", Options{Disk: "sda", Source: "out/"}, Options{}, "unsupported source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.in
			err := o.normalize()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if o.Disk != tc.want.Disk || o.Mode != tc.want.Mode || o.Hostname != tc.want.Hostname ||
				o.Timezone != tc.want.Timezone || o.Source != tc.want.Source || o.Channel != tc.want.Channel ||
				strings.Join(o.Libraries, ",") != strings.Join(tc.want.Libraries, ",") {
				t.Errorf("normalize = %+v, want %+v", o, tc.want)
			}
		})
	}
}

// The installer and PUT /system/hostname must agree on every name, or the
// wizard could set a name that Settings then refuses to keep.
func TestHostnameRuleMatchesSystem(t *testing.T) {
	for _, name := range []string{
		"vapor", "living-room", "a", "0", "x1", strings.Repeat("a", 63), strings.Repeat("a", 64),
		"localhost", "localhost2", "my-localhost", "", "-a", "a-", "a--b", "a_b", "a.b", "A", "é", "a b",
	} {
		inst, sys := checkHostname(name) == nil, system.ValidateHostname(name) == nil
		if inst != sys {
			t.Errorf("%q: the installer accepts it: %v, /system/hostname: %v", name, inst, sys)
		}
	}
}

func TestCheckTimezone(t *testing.T) {
	f := newFakeSys(t)
	if err := checkTimezone(paths.Zoneinfo, "Europe/Brussels"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"Europe", "Europe/Nowhere", "../etc", ""} {
		if err := checkTimezone(paths.Zoneinfo, bad); err == nil {
			t.Errorf("checkTimezone(%q) accepted", bad)
		}
	}
	_ = f
}

func TestLibraryMountpoints(t *testing.T) {
	for _, tc := range []struct{ label, uuid, want string }{
		{"SATA1TB", "u", "/var/mnt/SATA1TB"},
		{"My Games/2", "u", "/var/mnt/My_Games_2"},
		{"..", "1de1-77c4", "/var/mnt/1de1-77c4"},
		{"", "4760-BB01", "/var/mnt/4760-BB01"},
	} {
		if got := libraryMountpoint(tc.label, tc.uuid); got != tc.want {
			t.Errorf("libraryMountpoint(%q) = %q, want %q", tc.label, got, tc.want)
		}
	}
}
