package install

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestParseCLI(t *testing.T) {
	var stderr bytes.Buffer
	c, err := parseCLI([]string{
		"--disk", "/dev/sda", "--hostname", "den", "--user", "vapor", "--password", "vaporvapor",
		"--timezone=Europe/Brussels", "--library", "a-1", "--library", "b-2", "--mode", "repair",
		"--source", "http://builder:8000", "--yes",
	}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	want := Options{Disk: "/dev/sda", Mode: "repair", Hostname: "den", Password: "vaporvapor",
		Timezone: "Europe/Brussels", Source: "http://builder:8000"}
	libs := c.opts.Libraries
	c.opts.Libraries = nil
	if c.opts.Disk != want.Disk || c.opts.Mode != want.Mode || c.opts.Hostname != want.Hostname ||
		c.opts.Password != want.Password || c.opts.Timezone != want.Timezone || c.opts.Source != want.Source ||
		strings.Join(libs, ",") != "a-1,b-2" || !c.yes {
		t.Errorf("parseCLI = %+v %v", c, libs)
	}
	if !strings.Contains(stderr.String(), "--user is ignored") {
		t.Errorf("no --user notice: %q", stderr.String())
	}

	c, err = parseCLI([]string{"--disk", "sda"}, &stderr)
	if err != nil || c.yes || c.opts.Mode != ModeErase {
		t.Errorf("defaults: %+v %v", c, err)
	}
	if _, err := parseCLI([]string{"--disk", "sda", "extra"}, &stderr); err == nil {
		t.Error("stray argument accepted")
	}
	if _, err := parseCLI([]string{"--frobnicate"}, &stderr); err == nil {
		t.Error("unknown flag accepted")
	}
	if _, err := parseCLI([]string{"-h"}, &stderr); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: %v", err)
	}
}

func TestCLIProgress(t *testing.T) {
	var out bytes.Buffer
	p := &cliProgress{w: &out}
	p.report(StepWrite, 10, "Writing VaporOS 1 to /dev/sda2")
	for pct := 11; pct <= 75; pct++ {
		p.report(StepWrite, pct, "Writing slot a (1 MiB of 2 MiB)")
	}
	p.report(StepVerify, 75, "Verifying /dev/sda2")
	p.report(StepVerify, 88, "Checking the kernel and initramfs")
	p.report(StepDone, 100, "VaporOS 1 is installed on /dev/sda")
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// 1 + every 5% of 11..75 (11,16,...,71) + 3 distinct messages.
	if len(lines) != 1+13+3 {
		t.Errorf("printed %d lines:\n%s", len(lines), out.String())
	}
	if !strings.Contains(out.String(), "Checking the kernel") || !strings.HasSuffix(lines[len(lines)-1], "[100%] VaporOS 1 is installed on /dev/sda") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestSummary(t *testing.T) {
	f, img := installMachine(t)
	r := f.runner()
	in, err := newInstaller(f.env(r), Options{Disk: "sda", Hostname: "den", Libraries: []string{"1de127b9-77c4"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer in.close()
	if err := in.prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(in.summary(), "\n")
	for _, want := range []string{
		"/dev/sda (64.0 GiB, Test SSD)", img.man.Version, "2 x 8 GiB slots", "den",
		"set a password in the web UI", "SATA 1TB -> /var/mnt/SATA_1TB", "virtual display on DP-1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	if len(destructive(r)) > 0 {
		t.Error("prepare wrote to the disk")
	}
}
