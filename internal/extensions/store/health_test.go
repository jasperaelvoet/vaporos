package store

import (
	"os"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// A trial that passed is promoted whatever becomes of proven: one grown
// past its limit is rebuilt, and one that cannot be read at all is an error
// only after the promotion.
func TestAfterHealthyPromotesDespiteProven(t *testing.T) {
	proton := Mounted{ID: "proton", SHA256: hex64('1'), FSVerity: hex64('2')}
	rep := &BootReport{Mode: ModePending, Set: "1", Mounted: []Mounted{proton}}

	setup(t)
	writeSet(t, []string{"proton"}, nil, 1)
	check(t, setLink(config.ExtPendingLink(), "1"))
	junk := strings.Repeat("junk line\n", maxListFile/10+1) + "coolercontrol " + hex64('4') + "\n"
	writeFile(t, config.ExtProvenPath(), junk)
	check(t, AfterHealthy(rep))
	eq(t, "enabled", readLink(t, config.ExtEnabledLink()), "sets/1")
	p, err := Proven()
	check(t, err)
	if len(p) != 2 || !p[Pair{"proton", hex64('2')}] || !p[Pair{"coolercontrol", hex64('4')}] {
		t.Fatalf("proven = %v", p)
	}

	setup(t)
	writeSet(t, []string{"proton"}, nil, 1)
	check(t, setLink(config.ExtPendingLink(), "1"))
	check(t, os.MkdirAll(config.ExtProvenPath(), 0o755))
	if err := AfterHealthy(rep); err == nil {
		t.Error("no error for a proven that cannot be read")
	}
	eq(t, "enabled", readLink(t, config.ExtEnabledLink()), "sets/1")
	eq(t, "pending", readLink(t, config.ExtPendingLink()), "")
}

func TestTrialOK(t *testing.T) {
	setup(t)
	got, err := TrialOK()
	check(t, err)
	eq(t, "none", got, "")
	check(t, WriteTrialOK("12"))
	got, err = TrialOK()
	check(t, err)
	eq(t, "written", got, "12")
	if err := WriteTrialOK("../x"); err == nil {
		t.Error("WriteTrialOK took a bad set name")
	}
	writeFile(t, config.ExtTrialOKPath(), "sets/3\n")
	got, err = TrialOK()
	check(t, err)
	eq(t, "junk", got, "")
}

func TestAfterHealthy(t *testing.T) {
	proton := Mounted{ID: "proton", SHA256: hex64('1'), FSVerity: hex64('2')}
	cc := Mounted{ID: "coolercontrol", SHA256: hex64('3'), FSVerity: hex64('4')}
	tests := []struct {
		name        string
		rep         *BootReport
		pendingSet  string // the set pending names before; "1" is {proton, coolercontrol}
		want        string // AfterHealthyWant's fingerprint
		wantProven  int
		wantPromote bool
	}{
		{name: "nil report"},
		{name: "off", rep: &BootReport{Mode: ModeOff, Reason: ReasonCmdline}, pendingSet: "1"},
		{name: "enabled", rep: &BootReport{Mode: ModeEnabled, Set: "1", Mounted: []Mounted{proton}}, pendingSet: "1", wantProven: 1},
		{name: "os trial", rep: &BootReport{Mode: ModeOSTrial, Set: "1", Mounted: []Mounted{proton, cc}}, pendingSet: "1", wantProven: 2},
		{
			name:       "trial, all mounted",
			rep:        &BootReport{Mode: ModePending, Set: "1", Mounted: []Mounted{proton, cc}},
			pendingSet: "1", wantProven: 2, wantPromote: true,
		},
		{
			name:       "trial, all mounted, still desired",
			rep:        &BootReport{Mode: ModePending, Set: "1", Mounted: []Mounted{cc, proton}},
			want:       Fingerprint([]Pair{{"proton", hex64('2')}, {"coolercontrol", hex64('4')}}, nil),
			pendingSet: "1", wantProven: 2, wantPromote: true,
		},
		{
			name:       "trial, all mounted, no longer desired",
			rep:        &BootReport{Mode: ModePending, Set: "1", Mounted: []Mounted{proton, cc}},
			want:       Fingerprint([]Pair{{"proton", hex64('2')}}, nil),
			pendingSet: "1", wantProven: 2,
		},
		{
			name: "trial, one not in this catalog",
			rep: &BootReport{Mode: ModePending, Set: "1", Mounted: []Mounted{proton},
				Skipped: []Skipped{{ID: "coolercontrol", Reason: SkipNotInCatalog}}},
			pendingSet: "1", wantProven: 1, wantPromote: true,
		},
		{
			name: "trial, one missing",
			rep: &BootReport{Mode: ModePending, Set: "1", Mounted: []Mounted{proton},
				Skipped: []Skipped{{ID: "coolercontrol", Reason: SkipMissing}}},
			pendingSet: "1", wantProven: 1,
		},
		{
			name:       "trial, one not reported",
			rep:        &BootReport{Mode: ModePending, Set: "1", Mounted: []Mounted{proton}},
			pendingSet: "1", wantProven: 1,
		},
		{
			name:       "trial, pending moved on",
			rep:        &BootReport{Mode: ModePending, Set: "1", Mounted: []Mounted{proton, cc}},
			pendingSet: "2", wantProven: 2,
		},
		{
			name:       "trial of a set that is gone",
			rep:        &BootReport{Mode: ModePending, Set: "9", Mounted: []Mounted{proton}},
			pendingSet: "1", wantProven: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setup(t)
			writeSet(t, []string{"proton", "coolercontrol"}, nil, 1)
			writeSet(t, []string{"proton"}, nil, 2)
			if tt.pendingSet != "" {
				check(t, setLink(config.ExtPendingLink(), tt.pendingSet))
			}
			if tt.want == "" {
				check(t, AfterHealthy(tt.rep))
			} else {
				check(t, AfterHealthyWant(tt.rep, tt.want))
			}

			proven, err := Proven()
			check(t, err)
			eq(t, "proven", len(proven), tt.wantProven)
			if tt.wantProven > 0 && !proven[Pair{"proton", hex64('2')}] {
				t.Errorf("proton not proven: %v", proven)
			}
			if tt.wantPromote {
				eq(t, "enabled", readLink(t, config.ExtEnabledLink()), "sets/1")
				eq(t, "pending", readLink(t, config.ExtPendingLink()), "")
			} else {
				eq(t, "enabled", readLink(t, config.ExtEnabledLink()), "")
				want := ""
				if tt.pendingSet != "" {
					want = "sets/" + tt.pendingSet
				}
				eq(t, "pending", readLink(t, config.ExtPendingLink()), want)
			}
		})
	}
}
