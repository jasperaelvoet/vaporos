package drm

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fakeDebugfs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	saveDbg, saveSys := DebugfsDRI, SysClassDRM
	t.Cleanup(func() { DebugfsDRI, SysClassDRM = saveDbg, saveSys })
	DebugfsDRI = filepath.Join(dir, "debug/dri")
	SysClassDRM = filepath.Join(dir, "class/drm")
	return dir
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOverrideEDIDByMinor(t *testing.T) {
	fakeDebugfs(t)
	file := filepath.Join(DebugfsDRI, "1/DP-1/edid_override")
	touch(t, file)
	edid := []byte{0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0}
	if err := OverrideEDID("card1", "DP-1", edid); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); !bytes.Equal(got, edid) {
		t.Fatalf("edid_override = %x, want %x", got, edid)
	}
}

func TestOverrideEDIDByPCIAddress(t *testing.T) {
	fakeDebugfs(t)
	dev := filepath.Join(SysClassDRM, "card1/device")
	touch(t, filepath.Join(dev, "uevent"))
	if err := os.WriteFile(filepath.Join(dev, "uevent"), []byte("DRIVER=amdgpu\nPCI_SLOT_NAME=0000:0b:00.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(DebugfsDRI, "0000:0b:00.0/DP-1/edid_override")
	touch(t, file)
	if err := OverrideEDID("card1", "DP-1", []byte{1}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("edid_override = %x", got)
	}
}

func TestOverrideEDIDMissing(t *testing.T) {
	fakeDebugfs(t)
	if err := OverrideEDID("card1", "DP-1", []byte{1}); !errors.Is(err, ErrNoOverride) {
		t.Fatalf("err = %v, want ErrNoOverride", err)
	}
}

func TestReprobe(t *testing.T) {
	fakeDebugfs(t)
	status := filepath.Join(SysClassDRM, "card1-DP-1/status")
	touch(t, status)
	if err := Reprobe("card1", "DP-1"); err != nil {
		t.Fatal(err)
	}
	// The fake keeps the last write; the real file reads "connected".
	if got, _ := os.ReadFile(status); string(got) != "on" {
		t.Fatalf("status = %q, want the force back at on", got)
	}
	if err := Reprobe("card1", "DP-9"); err == nil {
		t.Fatal("Reprobe of a missing connector succeeded")
	}
}
