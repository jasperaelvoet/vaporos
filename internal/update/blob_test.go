package update

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOpenSourceAt(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	d := "sha256:" + strings.Repeat("a", 64)
	for _, c := range []struct{ spec, want string }{
		{"oci://ghcr.io/jasperaelvoet/vaporos", "oci://ghcr.io/jasperaelvoet/vaporos:20260901.000000"},
		{"oci://ghcr.io/jasperaelvoet/vaporos:main", "oci://ghcr.io/jasperaelvoet/vaporos:20260901.000000"},
		{"oci://ghcr.io/a/b@" + d, "oci://ghcr.io/a/b:20260901.000000"},
		{"oci://ghcr.io/a/b:main@" + d, "oci://ghcr.io/a/b:20260901.000000"},
		{"oci+http://10.0.0.2:5000/vos:dev", "oci+http://10.0.0.2:5000/vos:20260901.000000"},
		{" oci+http://10.0.0.2:5000/vos ", "oci+http://10.0.0.2:5000/vos:20260901.000000"},
		{dir, dir},
	} {
		src, err := OpenSourceAt(c.spec, bootedVersion)
		if err != nil {
			t.Errorf("%s: %v", c.spec, err)
			continue
		}
		if src.String() != c.want {
			t.Errorf("%s: opened %s, want %s", c.spec, src, c.want)
		}
	}
	for _, v := range []string{"", "a/b", "../x"} {
		if _, err := OpenSourceAt("oci://ghcr.io/a/b", v); err == nil {
			t.Errorf("version %q accepted", v)
		}
	}
}

func TestFetchBlobWithoutTheTag(t *testing.T) {
	e := setup(t)
	img := e.makeImage("20260930.000000", 200, 300<<10, nil)
	f := newFakeRegistry(t, img)
	f.failRoot.Store(true)

	// The tag is gone; the blob is still in the repository.
	src, err := OpenSourceAt(f.spec(), "20260101.000000")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := src.Manifest(ctx); err == nil || !strings.Contains(err.Error(), "no such tag") {
		t.Fatalf("manifest of a missing tag: %v", err)
	}
	a := img.m.Artifacts["root"]
	var buf bytes.Buffer
	var last int64
	err = src.FetchBlob(ctx, a.SHA256, a.Size, &buf, func(done int64) error { last = done; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), img.root()) || last != a.Size {
		t.Fatalf("got %d bytes (progress %d), want the root's %d", buf.Len(), last, a.Size)
	}
	// It resumed half way, from a fresh redirect.
	if n := f.rootBlobReqs.Load(); n != 2 {
		t.Fatalf("blob requests: %d", n)
	}
}

func TestFetchBlobRejects(t *testing.T) {
	e := setup(t)
	img := e.makeImage("20260930.000000", 200, 1000, nil)
	f := newFakeRegistry(t, img)
	src, _ := OpenSourceAt(f.spec(), "main")
	ctx := context.Background()
	a := img.m.Artifacts["root"]

	start := time.Now()
	if err := src.FetchBlob(ctx, strings.Repeat("0", 64), 10, &bytes.Buffer{}, nil); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("missing blob: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("a 404 was retried")
	}
	if err := src.FetchBlob(ctx, a.SHA256, a.Size-1, &bytes.Buffer{}, nil); !errors.Is(err, ErrChecksum) {
		t.Fatalf("short read: %v", err)
	}
	if err := src.FetchBlob(ctx, "nothex", 1, &bytes.Buffer{}, nil); err == nil {
		t.Fatal("an invalid digest was fetched")
	}

	dir, _ := OpenSourceAt(e.srcDir(img), "main")
	if err := dir.FetchBlob(ctx, a.SHA256, a.Size, &bytes.Buffer{}, nil); !errors.Is(err, ErrNoBlobs) {
		t.Fatalf("directory source: %v", err)
	}
}

func TestWithLock(t *testing.T) {
	setup(t)
	ran := false
	err := WithLock(context.Background(), func() error {
		ran = true
		// A stage meanwhile finds the lock held.
		if _, err := takeUpdateLock(context.Background(), lockPatience); !errors.Is(err, ErrBusy) {
			t.Errorf("lock while held: %v", err)
		}
		return errors.New("fn failed")
	})
	if !ran || err == nil || err.Error() != "fn failed" {
		t.Fatalf("ran %v, err %v", ran, err)
	}
	l, err := takeUpdateLock(context.Background(), lockPatience)
	if err != nil {
		t.Fatalf("the lock stayed held: %v", err)
	}
	l.Unlock()
}
