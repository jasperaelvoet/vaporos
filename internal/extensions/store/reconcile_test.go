package store

import (
	"slices"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
)

var testCatalog = &catalog.Catalog{Dispatcher: 1, Entries: []catalog.Entry{
	{ID: "proton", SHA256: hex64('1'), Size: 1, FSVerity: hex64('2'), Core: true},
	{ID: "coolercontrol", SHA256: hex64('3'), Size: 1, FSVerity: hex64('4')},
	{ID: "truckersmp", SHA256: hex64('5'), Size: 1, FSVerity: hex64('6'), Requires: []string{"proton"}},
}}

func mounted(ids ...string) []Mounted {
	var out []Mounted
	for _, id := range ids {
		e, _ := testCatalog.Get(id)
		out = append(out, Mounted{ID: id, SHA256: e.SHA256, FSVerity: e.FSVerity})
	}
	return out
}

func haveAllBut(ids ...string) func(catalog.Entry) bool {
	return func(e catalog.Entry) bool { return !slices.Contains(ids, e.ID) }
}

func TestPlanReconcile(t *testing.T) {
	ccOption := "options amdgpu ppfeaturemask=0x4000"
	ccOptions := func(ids []string) []string {
		if slices.Contains(ids, "coolercontrol") {
			return []string{ccOption, "not an option"}
		}
		return nil
	}
	set1 := &Set{Name: "1", IDs: []string{"proton"}}
	bootedProton := &BootReport{Mode: ModeEnabled, Set: "1", Mounted: mounted("proton")}
	fpTruckers := Fingerprint([]Pair{{"proton", hex64('2')}, {"truckersmp", hex64('6')}}, nil)

	tests := []struct {
		name        string
		in          ReconcileInput
		want        string
		wantIDs     []string
		wantMissing []string
	}{
		{
			name:    "booted set is desired",
			in:      ReconcileInput{Report: bootedProton, BootedSet: set1},
			want:    ActionNone,
			wantIDs: []string{"proton"},
		},
		{
			name: "booted set is desired, a stale pending goes",
			in: ReconcileInput{Report: bootedProton, BootedSet: set1,
				Pending: &Set{Name: "2", IDs: []string{"proton", "truckersmp"}, Tries: 2}},
			want:    ActionClearPending,
			wantIDs: []string{"proton"},
		},
		{
			name: "pending is desired",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1,
				Pending: &Set{Name: "2", IDs: []string{"truckersmp", "proton"}, Tries: 2}},
			want:    ActionKeepPending,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name: "pending is desired, options in another order",
			in: ReconcileInput{Wanted: []string{"coolercontrol"}, Report: bootedProton, BootedSet: set1,
				Options: func([]string) []string { return []string{"options it87 x=1", ccOption} },
				Pending: &Set{Name: "2", IDs: []string{"proton", "coolercontrol"}, Options: []string{ccOption, "options it87 x=1"}, Tries: 2}},
			want:    ActionKeepPending,
			wantIDs: []string{"proton", "coolercontrol"},
		},
		{
			name: "this boot is the pending trial",
			in: ReconcileInput{Wanted: []string{"truckersmp"},
				Report:    &BootReport{Mode: ModePending, Set: "2", TriesLeft: 1, Mounted: mounted("proton", "truckersmp")},
				BootedSet: &Set{Name: "2", IDs: []string{"proton", "truckersmp"}, Tries: 1},
				Pending:   &Set{Name: "2", IDs: []string{"proton", "truckersmp"}, Tries: 1}},
			want:    ActionKeepPending,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name: "this boot is the last try of the pending trial",
			in: ReconcileInput{Wanted: []string{"truckersmp"},
				Report:    &BootReport{Mode: ModePending, Set: "2", Mounted: mounted("proton", "truckersmp")},
				BootedSet: &Set{Name: "2", IDs: []string{"proton", "truckersmp"}},
				Pending:   &Set{Name: "2", IDs: []string{"proton", "truckersmp"}}},
			want:    ActionKeepPending,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name: "pending used up its tries",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1,
				Pending: &Set{Name: "2", IDs: []string{"proton", "truckersmp"}}},
			want:    ActionFailPending,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name: "pending used up its tries, booted without extensions",
			in: ReconcileInput{Report: &BootReport{Mode: ModeOff, Reason: ReasonNoExt},
				Pending: &Set{Name: "2", IDs: []string{"proton"}}},
			want:    ActionFailPending,
			wantIDs: []string{"proton"},
		},
		{
			name: "desired set failed before",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1,
				Failed: map[string]bool{fpTruckers: true}},
			want:    ActionBlocked,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name:    "new set",
			in:      ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1},
			want:    ActionPropose,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name: "new set replaces another pending",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1,
				Pending: &Set{Name: "2", IDs: []string{"proton", "coolercontrol"}, Tries: 2}},
			want:    ActionPropose,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name: "wanted image not sealed yet",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1,
				Have: haveAllBut("truckersmp")},
			want:        ActionNone,
			wantIDs:     []string{"proton"},
			wantMissing: []string{"truckersmp"},
		},
		{
			name: "requirement not sealed",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1,
				Have: haveAllBut("proton")},
			want:        ActionPropose,
			wantMissing: []string{"proton"},
		},
		{
			name:    "wanted id the catalog does not list",
			in:      ReconcileInput{Wanted: []string{"star-citizen"}, Report: bootedProton, BootedSet: set1},
			want:    ActionNone,
			wantIDs: []string{"proton"},
		},
		{
			name: "options changed",
			in: ReconcileInput{Wanted: []string{"coolercontrol"}, Options: ccOptions,
				Report:    &BootReport{Mode: ModeEnabled, Set: "1", Mounted: mounted("proton", "coolercontrol")},
				BootedSet: &Set{Name: "1", IDs: []string{"proton", "coolercontrol"}}},
			want:    ActionPropose,
			wantIDs: []string{"proton", "coolercontrol"},
		},
		{
			name: "options unchanged",
			in: ReconcileInput{Wanted: []string{"coolercontrol"}, Options: ccOptions,
				Report:    &BootReport{Mode: ModeEnabled, Set: "1", Mounted: mounted("coolercontrol", "proton")},
				BootedSet: &Set{Name: "1", IDs: []string{"proton", "coolercontrol"}, Options: []string{ccOption}}},
			want:    ActionNone,
			wantIDs: []string{"proton", "coolercontrol"},
		},
		{
			name: "booted at other digests",
			in: ReconcileInput{BootedSet: set1, Report: &BootReport{Mode: ModeEnabled, Set: "1",
				Mounted: []Mounted{{ID: "proton", SHA256: hex64('8'), FSVerity: hex64('9')}}}},
			want:    ActionPropose,
			wantIDs: []string{"proton"},
		},
		{
			name: "unproven image skipped this boot",
			in: ReconcileInput{BootedSet: set1, Report: &BootReport{Mode: ModeEnabled, Set: "1",
				Skipped: []Skipped{{ID: "proton", Reason: SkipUnproven}}}},
			want:    ActionPropose,
			wantIDs: []string{"proton"},
		},
		{
			name:    "no report",
			in:      ReconcileInput{},
			want:    ActionPropose,
			wantIDs: []string{"proton"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			in.Catalog = testCatalog
			if in.Have == nil {
				in.Have = haveAllBut()
			}
			p := PlanReconcile(in)
			eq(t, "action", p.Action, tt.want)
			eq(t, "ids", strs(p.IDs), strs(tt.wantIDs))
			var missing []string
			for _, e := range p.Missing {
				missing = append(missing, e.ID)
			}
			eq(t, "missing", strs(missing), strs(tt.wantMissing))
			eq(t, "fingerprint", p.Fingerprint, Fingerprint(Pairs(testCatalog, p.IDs), p.Options))
		})
	}
}

func TestPlanWantAndOptions(t *testing.T) {
	var asked []string
	p := PlanReconcile(ReconcileInput{
		Catalog: testCatalog,
		Wanted:  []string{"truckersmp", "coolercontrol", "truckersmp"},
		Have:    haveAllBut("coolercontrol"),
		Options: func(ids []string) []string {
			asked = ids
			return []string{"options b x=1", "options a y=2", "options a y=2", "junk"}
		},
	})
	eq(t, "want", strs(p.Want), strs([]string{"proton", "coolercontrol", "truckersmp"}))
	eq(t, "options asked for", strs(asked), strs([]string{"proton", "truckersmp"}))
	eq(t, "options", strs(p.Options), strs([]string{"options a y=2", "options b x=1"}))

	empty := PlanReconcile(ReconcileInput{})
	eq(t, "empty catalog", empty.Action, ActionNone)
	eq(t, "empty want", len(empty.Want), 0)
}

func TestRestartNeeded(t *testing.T) {
	all := func(string) bool { return true }
	pending := &Set{Name: "2", IDs: []string{"proton", "truckersmp"}, Tries: 2}
	normal := &BootReport{Mode: ModeEnabled, Set: "1"}
	tests := []struct {
		name    string
		rep     *BootReport
		pending *Set
		has     func(string) bool
		want    bool
	}{
		{"pending ready", normal, pending, all, true},
		{"removal ready", normal, &Set{Name: "2", Tries: 2}, all, true},
		{"after an os trial", &BootReport{Mode: ModeOSTrial, Set: "1"}, pending, all, true},
		{"no report", nil, pending, all, true},
		{"no pending", normal, nil, all, false},
		{"no tries left", normal, &Set{Name: "2", IDs: []string{"proton"}}, all, false},
		{"this boot tries it", &BootReport{Mode: ModePending, Set: "2"}, pending, all, false},
		{"booted with vos.ext=0", &BootReport{Mode: ModeOff, Reason: ReasonNoExt}, pending, all, false},
		{"booted skip-once", &BootReport{Mode: ModeOff, Reason: ReasonSkipOnce}, pending, all, false},
		{"booted skip-once without verity", &BootReport{Mode: ModeOff, Reason: "skip-once no-verity"}, pending, all, false},
		{"booted with vos.ext=0 and skip-once", &BootReport{Mode: ModeOff, Reason: "cmdline skip-once"}, pending, all, false},
		{"after a boot without verity", &BootReport{Mode: ModeEnabled, Set: "1", Reason: "no-verity"}, pending, all, true},
		{"image missing", normal, pending, func(id string) bool { return id != "truckersmp" }, false},
	}
	for _, tt := range tests {
		eq(t, tt.name, RestartNeeded(tt.rep, tt.pending, tt.has), tt.want)
	}
}
