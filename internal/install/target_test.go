package install

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/storage"
)

// Tests never ask the real NICs (ethtool) whether Wake-on-LAN works.
func init() { wakeOnLANCapable = func() bool { return false } }

// The installer adopts exactly the filesystems the generator mounts
// (storage.LibraryFS): a library it wrote to config.json and the
// generator then refused would show up as adopted but never mount.
func TestResolveLibrariesUsesTheSharedRule(t *testing.T) {
	f := newFakeSys(t)
	f.scan = []storage.Disk{
		{Path: "/dev/sdc1", Parent: "sdc", UUID: "aaaa-1111", Label: "Old", FSType: "ext3"},
		{Path: "/dev/sdd1", Parent: "sdd", UUID: "dddd-4444", Label: "Win", FSType: "ntfs"},
		{Path: "/dev/sde1", Parent: "sde", UUID: "BBBB-2222", Label: "Stick", FSType: "exfat"},
		{Path: "/dev/sdf1", Parent: "sdf", UUID: "CCCC-3333", Label: "FAT", FSType: "vfat"},
	}
	e := f.env(nil)
	libs, err := resolveLibraries(context.Background(), e, []string{"aaaa-1111", "dddd-4444"}, "sda")
	if err != nil || len(libs) != 2 {
		t.Fatalf("ext3 and NTFS: %+v, %v", libs, err)
	}
	for _, u := range []string{"BBBB-2222", "CCCC-3333"} {
		if _, err := resolveLibraries(context.Background(), e, []string{u}, "sda"); err == nil || !strings.Contains(err.Error(), "cannot hold a game library") {
			t.Errorf("%s accepted: %v", u, err)
		}
	}
	for _, fs := range []string{"exfat", "vfat", "iso9660"} {
		if storage.LibraryFS(fs) {
			t.Errorf("probe offers %s libraries", fs)
		}
	}
	for _, fs := range []string{"ext2", "ext3", "ext4", "btrfs", "xfs", "f2fs", "ntfs"} {
		if !storage.LibraryFS(fs) {
			t.Errorf("probe skips %s libraries", fs)
		}
	}
}

// A new install switches idle shutdown on only where Wake-on-LAN can wake
// the machine again; a repair keeps what the user chose.
func TestMachineConfigIdleShutdownFollowsWakeOnLAN(t *testing.T) {
	f := newFakeSys(t)
	root := t.TempDir()
	read := func() config.Config {
		t.Helper()
		var c config.Config
		if err := config.ReadJSON(filepath.Join(root, config.ConfigPath()), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	in := &installer{env: f.env(nil), man: &manifest.Manifest{Channel: "main"}, opts: Options{Mode: ModeErase}}

	for _, capable := range []bool{true, false} {
		if err := in.writeConfig(root, func() bool { return capable }); err != nil {
			t.Fatal(err)
		}
		if c := read(); c.Power.IdleShutdown != capable || c.Power.IdleMinutes != 15 {
			t.Errorf("Wake-on-LAN %v: power = %+v", capable, c.Power)
		}
	}

	// Repair: the existing choice stays, whatever the NIC can do.
	in.opts.Mode = ModeRepair
	if err := in.writeConfig(root, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if c := read(); c.Power.IdleShutdown {
		t.Error("a repair changed idle_shutdown")
	}
}
