package extensions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// states plays gamescope's unit going through seq, staying in the last.
func states(seq ...string) func(context.Context) string {
	var mu sync.Mutex
	return func(context.Context) string {
		mu.Lock()
		defer mu.Unlock()
		st := seq[0]
		if len(seq) > 1 {
			seq = seq[1:]
		}
		return st
	}
}

// A unit on its way down may have run its own prepare before steam.json
// changed, so prepare runs once it is down; one that comes back up runs
// prepare itself, and one that never settles is left to its next start
// or stop.
func TestUnwrapWaitsForGamescope(t *testing.T) {
	for _, c := range []struct {
		seq  []string
		runs bool
		logs string
	}{
		{[]string{"inactive"}, true, ""},
		{[]string{"deactivating", "deactivating", "inactive"}, true, ""},
		{[]string{"deactivating", "failed"}, true, ""},
		{[]string{"", "activating", "inactive"}, true, ""},
		{[]string{"active"}, false, ""},
		{[]string{"deactivating", "activating", "active"}, false, ""},
		{[]string{"deactivating"}, false, `vos-gamescope.service stayed "deactivating" for 20ms`},
	} {
		l := captureLogs(t)
		runs := 0
		u := unwrap{state: states(c.seq...), every: time.Millisecond, limit: 20 * time.Millisecond,
			prepare: func(context.Context) (string, error) {
				runs++
				return "vos steam: prepare: done (dispatcher off); changed userdata/1/config/localconfig.vdf", nil
			}}
		u.run()
		if (runs == 1) != c.runs || runs > 1 {
			t.Errorf("%q: prepare ran %d times", c.seq, runs)
		}
		if c.logs != "" && l.count(c.logs) != 1 {
			t.Errorf("%q: log\n%s", c.seq, l.buf.String())
		}
	}
}

// vosd logs what prepare said it did, never that it unwrapped: prepare
// exits 0 also when it skipped.
func TestUnwrapLogsWhatPrepareSaid(t *testing.T) {
	run := func(out string, err error) *logs {
		t.Helper()
		l := captureLogs(t)
		u := unwrap{state: states("inactive"), every: time.Millisecond, limit: time.Millisecond,
			prepare: func(context.Context) (string, error) { return out, err }}
		u.run()
		return l
	}
	l := run("vos steam: prepare: config.vdf: compatibility tool of app 227300: kept\n"+
		"vos steam: prepare: skipped: Steam is running; its files are left alone\n", nil)
	if l.count("extensions: vos steam prepare as vapor: vos steam: prepare: skipped: Steam is running") != 1 ||
		l.count("extensions: vos steam prepare as vapor: vos steam: prepare: config.vdf") != 1 ||
		l.count("took the dispatcher out") != 0 {
		t.Errorf("log:\n%s", l.buf.String())
	}

	var many strings.Builder
	for i := range 25 {
		fmt.Fprintf(&many, "vos steam: prepare: line %d %s\n", i, strings.Repeat("x", 400))
	}
	many.WriteString("vos steam: prepare: done; nothing needed changing\x00\r")
	l = run(many.String(), nil)
	lines := strings.Split(strings.TrimSpace(l.buf.String()), "\n")
	if len(lines) != maxPrepareLines+1 || l.count("16 earlier lines left out") != 1 ||
		!strings.HasSuffix(lines[len(lines)-1], "prepare: done; nothing needed changing") || l.count("line 15 ") != 0 {
		t.Errorf("%d lines:\n%s", len(lines), l.buf.String())
	}
	for _, line := range lines {
		if len(line) > 400 {
			t.Errorf("line of %d bytes", len(line))
		}
	}

	l = run("", fmt.Errorf("runuser -u vapor -- env ... /usr/bin/vos steam prepare: %w: ", errors.New("signal: killed")))
	if l.count("vos steam prepare as vapor said nothing") != 1 || l.count("extensions: vos steam prepare as vapor: signal: killed") != 1 ||
		l.count("runuser") != 0 {
		t.Errorf("log:\n%s", l.buf.String())
	}
}
