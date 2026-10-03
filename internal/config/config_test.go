package config

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestMutateSavesAndSnapshotCopies(t *testing.T) {
	StateDir = t.TempDir()
	c := Defaults()
	if err := c.Mutate(func(c *Config) {
		c.Storage.Libraries = append(c.Storage.Libraries, Library{UUID: "u1", Mountpoint: "/var/mnt/a"})
		c.Power.IdleMinutes = 30
	}); err != nil {
		t.Fatal(err)
	}
	snap := c.Snapshot()
	snap.Storage.Libraries[0].UUID = "changed"
	if c.Snapshot().Storage.Libraries[0].UUID != "u1" {
		t.Error("Snapshot shares the libraries slice with the live config")
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Power.IdleMinutes != 30 || len(got.Storage.Libraries) != 1 {
		t.Errorf("saved config = %+v", got)
	}
}

// Run with -race: concurrent updates from several services must not race.
func TestConcurrentUpdates(t *testing.T) {
	StateDir = t.TempDir()
	c := Defaults()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			c.Mutate(func(c *Config) { c.Power.IdleMinutes = i + 1 })
		}()
		go func() {
			defer wg.Done()
			_ = c.Snapshot().Power.IdleMinutes
		}()
	}
	wg.Wait()
}

func TestUIScalingDefaultsOn(t *testing.T) {
	StateDir = t.TempDir()
	if !Defaults().Display.UIScaling {
		t.Fatal("Defaults: ui_scaling off")
	}
	// A config.json from before the setting keeps the default.
	if err := WriteFileAtomic(ConfigPath(), []byte(`{"schema":1,"display":{"virtual_connector":"DP-1","hdr":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Display.UIScaling || c.Display.HDR || c.Display.VirtualConnector != "DP-1" {
		t.Errorf("old file: display = %+v", c.Display)
	}
	if err := c.Mutate(func(c *Config) { c.Display.UIScaling = false }); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(); err != nil || c.Display.UIScaling {
		t.Errorf("saved false: ui_scaling = %v, %v", c.Display.UIScaling, err)
	}
	if ScreensPath() != filepath.Join(StateDir, "screens.json") {
		t.Errorf("ScreensPath = %s", ScreensPath())
	}
}
