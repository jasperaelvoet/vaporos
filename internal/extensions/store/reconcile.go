package store

import (
	"slices"

	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
)

// Reconcile actions, what the caller does with a Plan (under Lock).
const (
	ActionNone         = "none"          // the booted set is the desired one
	ActionClearPending = "clear-pending" // ... and a pending set is stale: ClearPending
	ActionKeepPending  = "keep-pending"  // the pending set is the desired one
	ActionBlocked      = "blocked"       // the desired set failed its trial: needs attention
	ActionPropose      = "propose"       // Propose(Plan.IDs, Plan.Options)
	ActionFailPending  = "fail-pending"  // FailPending, then plan again
)

// ReconcileInput is everything PlanReconcile looks at.
type ReconcileInput struct {
	Catalog   *catalog.Catalog            // the booted image's
	Wanted    []string                    // the user's ids (core is added)
	Have      func(catalog.Entry) bool    // the image is sealed in the store
	Options   func(ids []string) []string // module option lines their settings render
	Report    *BootReport
	BootedSet *Set // the set the report names; nil when none
	Pending   *Set
	Failed    map[string]bool // fingerprints
}

// Plan is what reconcile should do.
type Plan struct {
	Want        []string        // wanted ∪ core with requirements, catalog order, listed ids only
	Missing     []catalog.Entry // entries of Want whose image is not sealed: fetch them
	IDs         []string        // the desired set: Want with sealed images and their requirements met
	Options     []string        // its module options, normalized
	Fingerprint string          // of IDs at the catalog's digests and Options
	Action      string
}

// PlanReconcile decides, without I/O, how the store should change. The
// desired set is wanted ∪ core with their requirements, as the catalog
// lists them, limited to sealed images. Equal to what booted (mounted ids
// and digests, the booted set's options): no pending, unless this boot is
// the pending set's trial. Equal to pending: left alone. Its fingerprint
// failed before: blocked. Otherwise proposed. A pending set with no tries
// left that this boot did not try fails first.
func PlanReconcile(in ReconcileInput) Plan {
	cat := in.Catalog
	p := Plan{Want: cat.Closure(append(slices.Clone(in.Wanted), cat.Core()...))}
	desired := map[string]bool{}
	for _, id := range p.Want {
		e, _ := cat.Get(id)
		if in.Have == nil || !in.Have(e) {
			p.Missing = append(p.Missing, e)
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
	p.Action = planAction(in, p)
	return p
}

func planAction(in ReconcileInput, p Plan) string {
	pend := in.Pending
	trying := pend != nil && in.Report.IsTrial() && in.Report.Set == pend.Name
	if pend != nil && pend.Tries <= 0 && !trying {
		return ActionFailPending
	}
	var bootedOptions []string
	if in.BootedSet != nil {
		bootedOptions = in.BootedSet.Options
	}
	if p.Fingerprint == Fingerprint(in.Report.MountedPairs(), bootedOptions) {
		switch {
		case pend == nil:
			return ActionNone
		case trying:
			// This boot is the pending set's trial: `vos health` promotes
			// it, which needs pending still in place.
			return ActionKeepPending
		}
		return ActionClearPending
	}
	if pend != nil && sameIDs(pend.IDs, p.IDs) && slices.Equal(normOptions(pend.Options), p.Options) {
		return ActionKeepPending
	}
	if in.Failed[p.Fingerprint] {
		return ActionBlocked
	}
	return ActionPropose
}

func sameIDs(a, b []string) bool {
	a, b = cleanIDs(a), cleanIDs(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// RestartNeeded reports whether a restart would try the pending set: it has
// tries left, this boot did not try it, this boot did not skip extensions
// on purpose, and every image it needs is sealed (has).
func RestartNeeded(rep *BootReport, pending *Set, has func(id string) bool) bool {
	if pending == nil || pending.Tries <= 0 || has == nil {
		return false
	}
	if rep != nil && (pending.Name == rep.Set || rep.Reason == ReasonNoExt || rep.Reason == ReasonSkipOnce) {
		return false
	}
	for _, id := range pending.IDs {
		if !has(id) {
			return false
		}
	}
	return true
}
