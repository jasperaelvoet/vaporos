package install

import (
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/manifest"
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
		{"short password", Options{Disk: "sda", Password: "vapor"}, Options{}, "at least 8"},
		{"nul password", Options{Disk: "sda", Password: "12345678\x00"}, Options{}, "not valid"},
		{"traversal timezone", Options{Disk: "sda", Timezone: "../../etc/shadow"}, Options{}, "invalid timezone"},
		{"bad uuid", Options{Disk: "sda", Libraries: []string{"x/../y"}}, Options{}, "invalid filesystem UUID"},
		{"oci source", Options{Disk: "sda", Source: "oci://ghcr.io/jasperaelvoet/vaporos"}, Options{}, "not supported"},
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
				o.Timezone != tc.want.Timezone || o.Source != tc.want.Source ||
				strings.Join(o.Libraries, ",") != strings.Join(tc.want.Libraries, ",") {
				t.Errorf("normalize = %+v, want %+v", o, tc.want)
			}
		})
	}
}

func TestParseSource(t *testing.T) {
	for _, tc := range []struct{ in, kind, dir string }{
		{"", "live", ""},
		{"/srv/vos/out", "dir", "/srv/vos/out"},
		{"file:///srv/vos/out/", "dir", "/srv/vos/out"},
		{"https://example.com/vos/", "http", ""},
	} {
		s, err := parseSource(tc.in)
		if err != nil || s.kind != tc.kind || s.dir != tc.dir {
			t.Errorf("parseSource(%q) = %+v, %v", tc.in, s, err)
		}
	}
	for _, bad := range []string{"http://", "file://relative/x", "ftp://x/", "oci://ghcr.io/x"} {
		if _, err := parseSource(bad); err == nil {
			t.Errorf("parseSource(%q) accepted", bad)
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

func TestCheckManifest(t *testing.T) {
	good := func() *manifest.Manifest {
		a := manifest.Artifact{Name: "root.erofs", Size: 10, SHA256: strings.Repeat("ab", 32)}
		k, i := a, a
		k.Name, i.Name = "vmlinuz", "initramfs.img"
		return &manifest.Manifest{Schema: 1, Version: "20260929.123456", MinUpdater: 1,
			Artifacts: map[string]manifest.Artifact{"root": a, "kernel": k, "initrd": i}}
	}
	if err := checkManifest(good()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		mod  func(m *manifest.Manifest)
		want string
	}{
		{"schema", func(m *manifest.Manifest) { m.Schema = 2 }, "schema"},
		{"min_updater", func(m *manifest.Manifest) { m.MinUpdater = 2 }, "newer installer"},
		{"version path", func(m *manifest.Manifest) { m.Version = "../../EFI" }, "invalid version"},
		{"missing kernel", func(m *manifest.Manifest) { delete(m.Artifacts, "kernel") }, "no kernel"},
		{"name path", func(m *manifest.Manifest) {
			a := m.Artifacts["root"]
			a.Name = "../root.erofs"
			m.Artifacts["root"] = a
		}, "invalid name"},
		{"size", func(m *manifest.Manifest) {
			a := m.Artifacts["initrd"]
			a.Size = 0
			m.Artifacts["initrd"] = a
		}, "invalid size"},
		{"sha", func(m *manifest.Manifest) {
			a := m.Artifacts["root"]
			a.SHA256 = "abc"
			m.Artifacts["root"] = a
		}, "invalid sha256"},
	} {
		m := good()
		tc.mod(m)
		if err := checkManifest(m); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
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
