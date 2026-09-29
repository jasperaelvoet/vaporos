//go:build linux

package drm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchHotplugStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := WatchHotplug(ctx)
	if err != nil {
		t.Skipf("no uevent netlink here: %v", err)
	}
	cancel()
	select {
	case _, ok := <-ch:
		for ok { // drain anything that raced in
			_, ok = <-ch
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop")
	}
}

func TestOpenMissing(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "card0"), false); err == nil {
		t.Fatal("opened a missing card")
	}
}

// TestReadOnlySmoke queries a real card without master, the way vosd does.
// It needs a KMS device (a VM with virtio-gpu, or vkms) and root.
func TestReadOnlySmoke(t *testing.T) {
	cards, _ := Cards()
	if len(cards) == 0 || os.Geteuid() != 0 {
		t.Skip("needs a DRM card and root")
	}
	c, err := Open(cards[0].Dev, false)
	if err != nil {
		t.Skip(err)
	}
	defer c.Close()
	c.SetClientCap(CapUniversalPlanes, 1)
	res, err := c.Resources()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range res.Connectors {
		conn, err := c.Connector(id, false)
		if err != nil {
			t.Fatal(err)
		}
		s, err := c.ScanoutOf(conn.Name)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: connected=%v modes=%d scanout=%s active=%v", conn.Name, conn.Connected(), len(conn.Modes), s.Mode.String(), s.Active)
	}
	if _, err := c.PlaneIDs(); err != nil {
		t.Fatal(err)
	}
}
