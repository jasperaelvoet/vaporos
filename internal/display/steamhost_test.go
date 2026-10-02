package display

import (
	"testing"
	"time"
)

func TestParseUnitTimes(t *testing.T) {
	// `systemctl --user show --timestamp=us+utc -p InactiveExitTimestamp
	// -p ExecMainStartTimestamp vos-gamescope.service`
	job, main := parseUnitTimes("ExecMainStartTimestamp=Fri 2026-10-02 09:41:07.512034 UTC\nInactiveExitTimestamp=Fri 2026-10-02 09:41:04.087213 UTC\n")
	if want := time.Date(2026, 10, 2, 9, 41, 4, 87213000, time.UTC); !job.Equal(want) {
		t.Errorf("job %v, want %v", job, want)
	}
	if want := time.Date(2026, 10, 2, 9, 41, 7, 512034000, time.UTC); !main.Equal(want) {
		t.Errorf("main %v, want %v", main, want)
	}
	// Never started, or a systemd that prints something else.
	job, main = parseUnitTimes("ExecMainStartTimestamp=\nInactiveExitTimestamp=n/a\n")
	if !job.IsZero() || !main.IsZero() {
		t.Errorf("unset: %v %v", job, main)
	}
	if job, main = parseUnitTimes("ExecMainStartTimestamp=@1727862067\n"); !job.IsZero() || !main.IsZero() {
		t.Errorf("another format: %v %v", job, main)
	}
}
