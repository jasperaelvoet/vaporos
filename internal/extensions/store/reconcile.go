package store

import (
	"slices"

	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
)

// Reconcile actions: the one store write a Plan asks for (under Lock).
// After ActionClearPending and ActionFailPending the caller plans again, as
// the store changed under the plan.
const (
	ActionNone         = "none"          // nothing to write
	ActionClearPending = "clear-pending" // ClearPending: the pending set is not wanted, or is left over
	ActionKeepPending  = "keep-pending"  // the pending set is the desired one
	ActionBlocked      = "blocked"       // the desired set failed its trial and nothing is pending (Plan.Blocked)
	ActionPropose      = "propose"       // Propose(Plan.IDs, Plan.Options)
	ActionFailPending  = "fail-pending"  // FailPending(the booted catalog)
)

// ReconcileInput is everything PlanReconcile looks at.
type ReconcileInput struct {
	Catalog   *catalog.Catalog            // the booted image's
	Wanted    []string                    // the user's ids (core is added)
	Have      func(catalog.Entry) bool    // the image is sealed in the store
	Options   func(ids []string) []string // module option lines their settings render
	Report    *BootReport
	BootedSet *Set // the set the report names; nil when none
	// Enabled is the last good set: nil on a fresh install until its first
	// trial passes. Its ids stay desired while their images are missing,
	// a pending set naming it is a promotion cut short, and on a boot that
	// mounted nothing on purpose it stands in for what booted.
	Enabled *Set
	Pending *Set
	Failed  map[string]bool // fingerprints
}

// Plan is what reconcile should do.
type Plan struct {
	Want        []string        // wanted ∪ core with requirements, catalog order, listed ids only
	Missing     []catalog.Entry // entries of Want whose image is not sealed: fetch them
	IDs         []string        // the desired set (see PlanReconcile), catalog order
	Options     []string        // its module options, normalized
	Fingerprint string          // of IDs at the catalog's digests and Options
	Action      string
	// Rule is the reconcile rule that chose Action (1-6, as numbered in
	// docs/CONTRACTS.md "Reconcile"). Rules 1 and 2 deal with a pending set
	// left over or out of tries, which nothing a download brings changes.
	Rule int
	// Blocked: the desired set's fingerprint is in failed, so it is not
	// proposed; it needs attention ("Try again" removes the fingerprint).
	Blocked bool
}

// PlanReconcile decides, without I/O, how the store should change.
//
// The desired set is wanted ∪ core with their requirements, as the catalog
// lists them. An id joins it only with its image sealed, unless it (or an
// id that requires it) mounted this boot or is in the booted or the enabled
// set: a missing image never shrinks the set, it only holds back additions.
// An id whose requirements did not join stays out too.
//
// What booted is the mounted images and the booted set's options; on a boot
// that mounted nothing on purpose (mode off, or no report), the enabled set
// at the catalog's digests stands in for it. A pending set outside its own
// trial that names the booted or the enabled set is left over from a
// promotion cut short: cleared. A pending set with no tries left that this
// boot did not try fails, unless its trials ran at other digests (an OS
// trial) or could not mount one of its images (one not sealed now): then
// it is only cleared. Then: the desired set equal to what booted needs no
// pending, unless this boot is the trial of a pending set that booted whole
// (`vos health` promotes it). Equal to pending: left alone. Its fingerprint
// failed before: blocked, and a pending set goes. Otherwise proposed.
func PlanReconcile(in ReconcileInput) Plan {
	cat := in.Catalog
	p := Plan{Want: cat.Closure(append(slices.Clone(in.Wanted), cat.Core()...))}
	held := map[string]bool{}
	for _, id := range cat.Closure(established(in)) {
		held[id] = true
	}
	desired := map[string]bool{}
	for _, id := range p.Want {
		e, _ := cat.Get(id)
		sealed := in.Have != nil && in.Have(e)
		if !sealed {
			p.Missing = append(p.Missing, e)
		}
		if !sealed && !held[id] {
			continue
		}
		met := true
		for _, r := range e.Requires {
			met = met && desired[r]
		}
		if met {
			desired[id] = true
			p.IDs = append(p.IDs, id)
		}
	}
	if in.Options != nil {
		p.Options = normOptions(in.Options(slices.Clone(p.IDs)))
	}
	p.Fingerprint = Fingerprint(Pairs(cat, p.IDs), p.Options)
	p.Rule, p.Action = planAction(in, &p)
	return p
}

// established returns the ids that stay desired without a sealed image:
// those this boot mounted, and those of the booted and the enabled set.
func established(in ReconcileInput) []string {
	var ids []string
	if in.Report != nil {
		for _, m := range in.Report.Mounted {
			ids = append(ids, m.ID)
		}
	}
	for _, s := range []*Set{in.BootedSet, in.Enabled} {
		if s != nil {
			ids = append(ids, s.IDs...)
		}
	}
	return ids
}

// planAction returns the rule that applies, and its action.
func planAction(in ReconcileInput, p *Plan) (int, string) {
	rep, pend := in.Report, in.Pending
	trying := pend != nil && rep.IsTrial() && rep.Set == pend.Name
	if pend != nil && !trying {
		leftover := (rep != nil && pend.Name == rep.Set) || (in.Enabled != nil && pend.Name == in.Enabled.Name)
		switch {
		case leftover:
			return 1, ActionClearPending
		case pend.Tries <= 0 && rep != nil && rep.Mode == ModeOSTrial:
			return 2, ActionClearPending
		case pend.Tries <= 0 && !allSealed(in, pend.IDs):
			// Its trials could not mount that image: not the set's fault.
			return 2, ActionClearPending
		case pend.Tries <= 0:
			return 2, ActionFailPending
		}
	}
	booted := bootedFingerprint(in)
	if p.Fingerprint == booted {
		switch {
		case pend == nil:
			return 3, ActionNone
		case trying && Fingerprint(Pairs(in.Catalog, pend.IDs), pend.Options) == booted:
			// This boot is the pending set's trial and it booted whole:
			// `vos health` promotes it, which needs pending in place.
			return 3, ActionKeepPending
		}
		return 3, ActionClearPending
	}
	if pend != nil && sameIDs(pend.IDs, p.IDs) && slices.Equal(normOptions(pend.Options), p.Options) {
		return 4, ActionKeepPending
	}
	if in.Failed[p.Fingerprint] {
		p.Blocked = true
		if pend != nil {
			return 5, ActionClearPending
		}
		return 5, ActionBlocked
	}
	return 6, ActionPropose
}

// allSealed reports whether the image of every id of ids that the catalog
// lists is sealed in the store.
func allSealed(in ReconcileInput, ids []string) bool {
	for _, id := range ids {
		if e, ok := in.Catalog.Get(id); ok && (in.Have == nil || !in.Have(e)) {
			return false
		}
	}
	return true
}

// bootedFingerprint is the fingerprint of what this boot runs: the mounted
// images and the booted set's options, or the enabled set when this boot
// mounted nothing on purpose.
func bootedFingerprint(in ReconcileInput) string {
	if in.Report.off() {
		if in.Enabled == nil {
			return Fingerprint(nil, nil)
		}
		return Fingerprint(Pairs(in.Catalog, in.Enabled.IDs), in.Enabled.Options)
	}
	var options []string
	if in.BootedSet != nil {
		options = in.BootedSet.Options
	}
	return Fingerprint(in.Report.MountedPairs(), options)
}

func sameIDs(a, b []string) bool {
	a, b = cleanIDs(a), cleanIDs(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// RestartNeeded reports whether a restart would try the pending set: it has
// tries left, this boot did not try it, this boot did not skip extensions
// on purpose, nothing says the next boot would not try it either (tries
// that could not be written, or no report at all), and every image it
// needs is sealed (has).
func RestartNeeded(rep *BootReport, pending *Set, has func(id string) bool) bool {
	if rep == nil || pending == nil || pending.Tries <= 0 || has == nil || pending.Name == rep.Set {
		return false
	}
	for _, r := range []string{ReasonCmdline, ReasonSkipOnce, ReasonTriesWrite, ReasonNoReport} {
		if rep.HasReason(r) {
			return false
		}
	}
	for _, id := range pending.IDs {
		if !has(id) {
			return false
		}
	}
	return true
}
