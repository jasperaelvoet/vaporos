package sysd

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunOutputAndError(t *testing.T) {
	out, err := Run(context.Background(), "sh", "-c", "echo hello; echo oops >&2")
	if err != nil || out != "hello\noops" {
		t.Errorf("Run = %q, %v", out, err)
	}
	out, err = Run(context.Background(), "sh", "-c", "echo bad; exit 3")
	if err == nil || out != "bad" || !strings.HasPrefix(err.Error(), "sh -c echo bad; exit 3: exit status 3: bad") {
		t.Errorf("failing Run = %q, %v", out, err)
	}
}

// TestRunTimeoutWithOrphan: a command whose child outlives it keeps the
// output pipe open; Run still returns soon after its context ends.
func TestRunTimeoutWithOrphan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	// sh forks sleep (it is not the last command, so no exec); killing sh
	// leaves sleep holding the pipe for 6 s.
	_, err := Run(ctx, "sh", "-c", "sleep 6; true")
	if took := time.Since(start); took > 200*time.Millisecond+waitDelay+time.Second {
		t.Errorf("Run took %s after its context ended", took)
	}
	if err == nil {
		t.Error("a killed command succeeded")
	}
}

// TestGroupCommandKillsDescendants: on timeout the whole process group
// dies, so Run returns at once instead of after waitDelay.
func TestGroupCommandKillsDescendants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := run(groupCommand(ctx, "sh", "-c", "sleep 20; true"))
	if took := time.Since(start); took > time.Second+200*time.Millisecond {
		t.Errorf("group kill took %s", took)
	}
	if err == nil {
		t.Error("a killed command succeeded")
	}
	// A command that finishes by itself is unaffected.
	if out, err := run(groupCommand(context.Background(), "sh", "-c", "echo ok")); err != nil || out != "ok" {
		t.Errorf("run = %q, %v", out, err)
	}
}
