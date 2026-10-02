package store

import (
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

func TestAfterHealthy(t *testing.T) {
	proton := Mounted{ID: "proton", SHA256: hex64('1'), FSVerity: hex64('2')}
	cc := Mounted{ID: "coolercontrol", SHA256: hex64('3'), FSVerity: hex64('4')}
	tests := []struct {
		name        string
		rep         *BootReport
		pendingSet  string // the set pending names before; "1" is {proton, coolercontrol}
		wantProven  int
		wantPromote bool
	}{
		{name: "nil report"},
		{name: "off", rep: &BootReport{Mode: ModeOff, Reason: ReasonNoExt}, pendingSet: "1"},
		{name: "enabled", rep: &BootReport{Mode: ModeEnabled, Set: "1", Mounted: []Mounted{proton}}, pendingSet: "1", wantProven: 1},
		{name: "os trial", rep: &BootReport{Mode: ModeOSTrial, Set: "1", Mounted: []Mounted{proton, cc}}, pendingSet: "1", wantProven: 2},
		{
			name:       "trial, all mounted",
			rep:        &BootReport{Mode: ModePending, Set: "1", Mounted: []Mounted{proton, cc}},
			pendingSet: "1", wantProven: 2, wantPromote: true,
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
			check(t, AfterHealthy(tt.rep))

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
