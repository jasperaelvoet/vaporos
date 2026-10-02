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
	fpCC := Fingerprint([]Pair{{"proton", hex64('2')}, {"coolercontrol", hex64('4')}}, nil)
	trial2 := func(tries int, ids ...string) *BootReport {
		return &BootReport{Mode: ModePending, Set: "2", TriesLeft: tries, Mounted: mounted(ids...)}
	}
	set2 := func(tries int, ids ...string) *Set { return &Set{Name: "2", IDs: ids, Tries: tries} }

	tests := []struct {
		name        string
		in          ReconcileInput
		want        string
		wantIDs     []string
		wantMissing []string
		wantBlocked bool
	}{
		{
			name:    "booted set is desired",
			in:      ReconcileInput{Report: bootedProton, BootedSet: set1, Enabled: set1},
			want:    ActionNone,
			wantIDs: []string{"proton"},
		},
		{
			name: "booted set is desired, a stale pending goes",
			in: ReconcileInput{Report: bootedProton, BootedSet: set1, Enabled: set1,
				Pending: set2(2, "proton", "truckersmp")},
			want:    ActionClearPending,
			wantIDs: []string{"proton"},
		},
		{
			name: "pending is desired",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1, Enabled: set1,
				Pending: set2(2, "truckersmp", "proton")},
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
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: trial2(1, "proton", "truckersmp"),
				BootedSet: set2(1, "proton", "truckersmp"), Enabled: set1, Pending: set2(1, "proton", "truckersmp")},
			want:    ActionKeepPending,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name: "this boot is the last try of the pending trial",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: trial2(0, "proton", "truckersmp"),
				BootedSet: set2(0, "proton", "truckersmp"), Enabled: set1, Pending: set2(0, "proton", "truckersmp")},
			want:    ActionKeepPending,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name: "trial of a set that did not boot whole, which is no longer wanted",
			in: ReconcileInput{
				Report: &BootReport{Mode: ModePending, Set: "2", TriesLeft: 1, Mounted: mounted("proton"),
					Skipped: []Skipped{{ID: "truckersmp", Reason: SkipMissing}}},
				BootedSet: set2(1, "proton", "truckersmp"), Enabled: set1, Pending: set2(1, "proton", "truckersmp")},
			want:    ActionClearPending,
			wantIDs: []string{"proton"},
		},
		{
			name: "trial of a set that did not boot whole, still wanted",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Have: haveAllBut("truckersmp"),
				Report: &BootReport{Mode: ModePending, Set: "2", TriesLeft: 1, Mounted: mounted("proton"),
					Skipped: []Skipped{{ID: "truckersmp", Reason: SkipMissing}}},
				BootedSet: set2(1, "proton", "truckersmp"), Enabled: set1, Pending: set2(1, "proton", "truckersmp")},
			want:        ActionKeepPending,
			wantIDs:     []string{"proton", "truckersmp"},
			wantMissing: []string{"truckersmp"},
		},
		{
			name: "trial of a set with an id this catalog does not list",
			in: ReconcileInput{
				Report: &BootReport{Mode: ModePending, Set: "2", TriesLeft: 1, Mounted: mounted("proton"),
					Skipped: []Skipped{{ID: "star-citizen", Reason: SkipNotInCatalog}}},
				BootedSet: set2(1, "proton", "star-citizen"), Enabled: set1, Pending: set2(1, "proton", "star-citizen")},
			want:    ActionKeepPending,
			wantIDs: []string{"proton"},
		},
		{
			name: "trial of a set no longer wanted",
			in: ReconcileInput{Wanted: []string{"coolercontrol"}, Report: trial2(1, "proton", "truckersmp"),
				BootedSet: set2(1, "proton", "truckersmp"), Enabled: set1, Pending: set2(1, "proton", "truckersmp")},
			want:    ActionPropose,
			wantIDs: []string{"proton", "coolercontrol"},
		},
		{
			name: "trial of a set no longer wanted, the desired one failed before",
			in: ReconcileInput{Wanted: []string{"coolercontrol"}, Report: trial2(1, "proton", "truckersmp"),
				BootedSet: set2(1, "proton", "truckersmp"), Enabled: set1, Pending: set2(1, "proton", "truckersmp"),
				Failed: map[string]bool{fpCC: true}},
			want:        ActionClearPending,
			wantIDs:     []string{"proton", "coolercontrol"},
			wantBlocked: true,
		},
		{
			name: "pending used up its tries",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1, Enabled: set1,
				Pending: set2(0, "proton", "truckersmp")},
			want:    ActionFailPending,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name: "pending used up its tries, booted without extensions",
			in: ReconcileInput{Report: &BootReport{Mode: ModeOff, Reason: ReasonCmdline},
				Pending: set2(0, "proton")},
			want:    ActionFailPending,
			wantIDs: []string{"proton"},
		},
		{
			name: "pending used up its tries before an OS update",
			in: ReconcileInput{Wanted: []string{"truckersmp"},
				Report:    &BootReport{Mode: ModeOSTrial, Set: "1", Mounted: mounted("proton")},
				BootedSet: set1, Enabled: set1, Pending: set2(0, "proton", "truckersmp")},
			want:    ActionClearPending,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name: "pending names the enabled set: a promotion cut short",
			in: ReconcileInput{Wanted: []string{"truckersmp"},
				Report:    &BootReport{Mode: ModeEnabled, Set: "2", Mounted: mounted("proton", "truckersmp")},
				BootedSet: set2(0, "proton", "truckersmp"), Enabled: set2(0, "proton", "truckersmp"),
				Pending: set2(0, "proton", "truckersmp")},
			want:    ActionClearPending,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name: "pending names the booted set outside its trial",
			in: ReconcileInput{Wanted: []string{"coolercontrol"},
				Report:    &BootReport{Mode: ModeOSTrial, Set: "2", Mounted: mounted("proton", "truckersmp")},
				BootedSet: set2(1, "proton", "truckersmp"), Enabled: set1, Pending: set2(1, "proton", "truckersmp")},
			want:    ActionClearPending,
			wantIDs: []string{"proton", "coolercontrol"},
		},
		{
			name: "desired set failed before",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1, Enabled: set1,
				Failed: map[string]bool{fpTruckers: true}},
			want:        ActionBlocked,
			wantIDs:     []string{"proton", "truckersmp"},
			wantBlocked: true,
		},
		{
			name: "desired set failed before, a stale pending goes",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1, Enabled: set1,
				Pending: set2(2, "proton", "coolercontrol"), Failed: map[string]bool{fpTruckers: true}},
			want:        ActionClearPending,
			wantIDs:     []string{"proton", "truckersmp"},
			wantBlocked: true,
		},
		{
			name:    "new set",
			in:      ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1, Enabled: set1},
			want:    ActionPropose,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name: "new set replaces another pending",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1, Enabled: set1,
				Pending: set2(2, "proton", "coolercontrol")},
			want:    ActionPropose,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name: "wanted image not sealed yet",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1, Enabled: set1,
				Have: haveAllBut("truckersmp")},
			want:        ActionNone,
			wantIDs:     []string{"proton"},
			wantMissing: []string{"truckersmp"},
		},
		{
			name: "requirement not sealed, but booted",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: bootedProton, BootedSet: set1, Enabled: set1,
				Have: haveAllBut("proton")},
			want:        ActionPropose,
			wantIDs:     []string{"proton", "truckersmp"},
			wantMissing: []string{"proton"},
		},
		{
			name: "requirement not sealed nor established",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: &BootReport{Mode: ModeEnabled, Reason: ReasonNoSet},
				Have: haveAllBut("proton")},
			want:        ActionNone,
			wantMissing: []string{"proton"},
		},
		{
			name: "booted image missing",
			in: ReconcileInput{Wanted: []string{"coolercontrol"}, Have: haveAllBut("coolercontrol"),
				Report:    &BootReport{Mode: ModeEnabled, Set: "1", Mounted: mounted("proton", "coolercontrol")},
				BootedSet: &Set{Name: "1", IDs: []string{"proton", "coolercontrol"}}},
			want:        ActionNone,
			wantIDs:     []string{"proton", "coolercontrol"},
			wantMissing: []string{"coolercontrol"},
		},
		{
			name: "mounted image missing, booted on an OS trial",
			in: ReconcileInput{Have: haveAllBut("proton"),
				Report: &BootReport{Mode: ModeOSTrial, Set: "1", Mounted: mounted("proton")}},
			want:        ActionNone,
			wantIDs:     []string{"proton"},
			wantMissing: []string{"proton"},
		},
		{
			name: "enabled images missing keep their requirements",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Have: haveAllBut("proton", "truckersmp"),
				Report:  &BootReport{Mode: ModeEnabled, Set: "1", Skipped: []Skipped{{ID: "proton", Reason: SkipMissing}, {ID: "truckersmp", Reason: SkipRequires}}},
				Enabled: &Set{Name: "1", IDs: []string{"truckersmp"}}, BootedSet: &Set{Name: "1", IDs: []string{"truckersmp"}}},
			want:        ActionPropose,
			wantIDs:     []string{"proton", "truckersmp"},
			wantMissing: []string{"proton", "truckersmp"},
		},
		{
			name:    "wanted id the catalog does not list",
			in:      ReconcileInput{Wanted: []string{"star-citizen"}, Report: bootedProton, BootedSet: set1, Enabled: set1},
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
			in: ReconcileInput{BootedSet: set1, Enabled: set1, Report: &BootReport{Mode: ModeEnabled, Set: "1",
				Skipped: []Skipped{{ID: "proton", Reason: SkipUnproven}}}},
			want:    ActionPropose,
			wantIDs: []string{"proton"},
		},
		{
			name:    "booted without extensions, enabled is desired",
			in:      ReconcileInput{Report: &BootReport{Mode: ModeOff, Reason: ReasonSkipOnce}, Enabled: set1},
			want:    ActionNone,
			wantIDs: []string{"proton"},
		},
		{
			name: "booted with vos.ext=0 and no verity, enabled is desired",
			in: ReconcileInput{Report: &BootReport{Mode: ModeOff, Reason: ReasonCmdline + " " + ReasonNoVerity},
				Enabled: &Set{Name: "1", IDs: []string{"coolercontrol", "proton"}, Options: []string{ccOption}},
				Wanted:  []string{"coolercontrol"}, Options: ccOptions},
			want:    ActionNone,
			wantIDs: []string{"proton", "coolercontrol"},
		},
		{
			name: "booted without extensions, a stale pending goes",
			in: ReconcileInput{Report: &BootReport{Mode: ModeOff, Reason: ReasonSkipOnce}, Enabled: set1,
				Pending: set2(2, "proton", "truckersmp")},
			want:    ActionClearPending,
			wantIDs: []string{"proton"},
		},
		{
			name: "booted without extensions, desired differs from enabled",
			in: ReconcileInput{Wanted: []string{"truckersmp"}, Report: &BootReport{Mode: ModeOff, Reason: ReasonSkipOnce},
				Enabled: set1},
			want:    ActionPropose,
			wantIDs: []string{"proton", "truckersmp"},
		},
		{
			name:    "no report, enabled is desired",
			in:      ReconcileInput{Enabled: set1},
			want:    ActionNone,
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
			eq(t, "blocked", p.Blocked, tt.wantBlocked)
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
		{"no verity", &BootReport{Mode: ModeEnabled, Set: "1", Reason: ReasonNoVerity}, pending, all, true},
		{"no pending", normal, nil, all, false},
		{"no tries left", normal, &Set{Name: "2", IDs: []string{"proton"}}, all, false},
		{"this boot tries it", &BootReport{Mode: ModePending, Set: "2"}, pending, all, false},
		{"booted with vos.ext=0", &BootReport{Mode: ModeOff, Reason: ReasonCmdline}, pending, all, false},
		{"booted skip-once", &BootReport{Mode: ModeOff, Reason: ReasonSkipOnce}, pending, all, false},
		{"booted skip-once without verity", &BootReport{Mode: ModeOff, Reason: ReasonNoVerity + " " + ReasonSkipOnce}, pending, all, false},
		{"image missing", normal, pending, func(id string) bool { return id != "truckersmp" }, false},
	}
	for _, tt := range tests {
		eq(t, tt.name, RestartNeeded(tt.rep, tt.pending, tt.has), tt.want)
	}
}
