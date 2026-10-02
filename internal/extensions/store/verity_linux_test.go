package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/fsverity"
)

// TestRealVerity runs the kernel calls on the test machine's temp dir. Most
// filesystems there (tmpfs, ext4 without the verity feature) cannot seal,
// and then it only checks that the failure reads as ErrUnsupported.
func TestRealVerity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image")
	data := bytes.Repeat([]byte("vaporos"), 3000)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if _, err := sysMeasureVerity(f); err != nil && !errors.Is(err, errNotSealed) && !errors.Is(err, ErrUnsupported) {
		t.Fatalf("measure of a plain file = %v", err)
	}
	err = sysEnableVerity(f)
	if errors.Is(err, ErrUnsupported) {
		t.Skipf("no fs-verity here: %v", err)
	}
	if err != nil {
		t.Fatalf("enable = %v", err)
	}
	sealed, err := sysVerityAttr(f)
	if err != nil || !sealed {
		t.Errorf("statx attribute = %v, %v", sealed, err)
	}
	got, err := sysMeasureVerity(f)
	if err != nil {
		t.Fatal(err)
	}
	want, err := fsverity.DigestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("kernel digest %s, Go digest %s", got, want)
	}
	if w, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		w.Close()
		t.Error("a sealed file opened for writing")
	}
}

// TestRealPutHas seals through the kernel where the temp dir can.
func TestRealPutHas(t *testing.T) {
	state, run := config.StateDir, config.RunDir
	config.StateDir, config.RunDir = t.TempDir(), t.TempDir()
	t.Cleanup(func() { config.StateDir, config.RunDir = state, run })

	img := newImage(t, "proton", 3*fsverity.BlockSize+17)
	err := Put(t.Context(), img.entry, serve(img.data))
	if errors.Is(err, ErrUnsupported) {
		t.Skipf("no fs-verity here: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := Has(img.entry); !ok || err != nil {
		t.Fatalf("Has = %v, %v", ok, err)
	}
	if w, err := os.OpenFile(ImagePath(img.entry.SHA256), os.O_WRONLY, 0); err == nil {
		w.Close()
		t.Error("a sealed image opened for writing")
	}
	wrong := img.entry
	wrong.FSVerity = hex64('0')
	if err := Put(t.Context(), wrong, serve(img.data)); !errors.Is(err, ErrMismatch) {
		t.Errorf("Put with a wrong digest = %v", err)
	}
	if ok, _ := Has(wrong); ok {
		t.Error("Has accepted a wrong digest")
	}

	// A plain copy under the final name is not sealed: deleted.
	other := newImage(t, "coolercontrol", 5000)
	if err := os.WriteFile(ImagePath(other.entry.SHA256), other.data, 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := Has(other.entry); ok || err != nil {
		t.Errorf("Has(unsealed) = %v, %v", ok, err)
	}
	if _, err := os.Stat(ImagePath(other.entry.SHA256)); !os.IsNotExist(err) {
		t.Errorf("unsealed image kept: %v", err)
	}
	noTemps(t, config.ExtImagesDir())
}
