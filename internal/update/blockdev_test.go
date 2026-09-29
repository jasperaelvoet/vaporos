package update

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
)

// These tests stage into a real block device instead of a file: the
// exclusive open, the size from seeking a device, and the read-back after
// dropping the page cache. They need root and a scratch device, so they
// only run when asked, for example in a privileged container:
//
//	truncate -s 4M /tmp/slot && losetup -f --show /tmp/slot   # /dev/loop0
//	VOS_TEST_SLOT_DEVICE=/dev/loop0 ./update.test -test.run BlockDevice
//	mkfs.ext4 -q /dev/loop0 && mount /dev/loop0 /mnt
//	VOS_TEST_MOUNTED_DEVICE=/dev/loop0 ./update.test -test.run MountedSlot
//
// Everything on the device is overwritten.

// blockSlot points slot b at the device named by env, or skips.
func blockSlot(t *testing.T, env string) (*testEnv, string) {
	dev := os.Getenv(env)
	if dev == "" {
		t.Skip("set " + env + " to a scratch block device to run")
	}
	fi, err := os.Stat(dev)
	if err != nil || fi.Mode()&os.ModeDevice == 0 {
		t.Fatalf("%s is not a device: %v", dev, err)
	}
	e := setup(t)
	e.must(os.Remove(e.slotDev("b")))
	e.must(os.Symlink(dev, e.slotDev("b")))
	return e, dev
}

func TestStageToBlockDevice(t *testing.T) {
	e, dev := blockSlot(t, "VOS_TEST_SLOT_DEVICE")
	img := e.makeImage(newVersion, 200, 3<<20+12345, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dev)
	e.must(err)
	defer f.Close()
	got := make([]byte, len(img.root()))
	if _, err := io.ReadFull(f, got); err != nil || !bytes.Equal(got, img.root()) {
		t.Fatalf("device content differs (%v)", err)
	}
	if en := e.entry("b"); en == nil || en.Version != newVersion {
		t.Fatalf("entry %+v", en)
	}

	// Bigger than the device: refused before anything is touched.
	big := e.makeImage("20261001.000000", 300, int(sizeOf(t, dev))+1, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(big)), Options{}); !errors.Is(err, ErrTooBig) {
		t.Fatalf("an image larger than the device: %v", err)
	}
	if en := e.entry("b"); en == nil || en.Version != newVersion {
		t.Fatalf("entry after the refusal: %+v", en)
	}
}

// A slot in use (mounted) is refused by the exclusive open, before its
// entry is touched.
func TestStageRefusesMountedSlot(t *testing.T) {
	e, _ := blockSlot(t, "VOS_TEST_MOUNTED_DEVICE")
	img := e.makeImage(newVersion, 200, 1000, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("a mounted slot: %v", err)
	}
	if en := e.entry("b"); en == nil || en.Version != oldIdleVersion {
		t.Fatalf("entry after the refusal: %+v", en)
	}
}

func sizeOf(t *testing.T, dev string) int64 {
	f, err := os.Open(dev)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
