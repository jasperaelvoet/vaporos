package extensions

import (
	"slices"

	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// Extension states in Status.
const (
	StateInstalled        = "installed"           // mounted this boot
	StateNotInstalled     = "not-installed"       // not mounted, and nothing to do about it
	StateDownloading      = "downloading"         // its image is being fetched for the booted version
	StateRestartNeeded    = "restart-needed"      // the pending set mounts it, or drops it, at the next restart
	StateNeedsAttention   = "needs-attention"     // wanted, but its image cannot be had or its set failed
	StateNotInThisVersion = "not-in-this-version" // wanted, but the booted image's catalog does not list it
)

// Status is the extensions as vosd sees them after its last pass.
// Provisional: the control center's milestone serves it over the API and
// settles its shape in docs/CONTRACTS.md.
type Status struct {
	Version       string            `json:"version"` // the booted image's
	Mode          string            `json:"mode"`    // the boot report's
	Reason        string            `json:"reason,omitempty"`
	Set           string            `json:"set,omitempty"`     // the set this boot used
	Pending       string            `json:"pending,omitempty"` // the set a restart tries
	RestartNeeded bool              `json:"restart_needed"`
	Busy          bool              `json:"busy"`
	Error         string            `json:"error,omitempty"`
	Extensions    []ExtensionStatus `json:"extensions"` // catalog order, then wanted ids it lacks
}

// ExtensionStatus is one extension in Status.
type ExtensionStatus struct {
	ID       string    `json:"id"`
	State    string    `json:"state"`
	Core     bool      `json:"core"`
	Wanted   bool      `json:"wanted"` // the user added it (core is always wanted)
	Mounted  bool      `json:"mounted"`
	Skipped  string    `json:"skipped,omitempty"` // why this boot did not mount it
	Progress *Progress `json:"progress,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// Progress is a download's bytes so far and in all.
type Progress struct {
	Bytes int64 `json:"bytes"`
	Total int64 `json:"total"`
}

// Status returns what the last pass saw, with the downloads under way.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.view
	st := Status{Version: v.version, RestartNeeded: v.restart, Busy: s.busyLocked(), Error: v.err,
		Extensions: []ExtensionStatus{}}
	if v.rep != nil {
		st.Mode, st.Reason, st.Set = v.rep.Mode, v.rep.Reason, v.rep.Set
	}
	var pending []string
	if v.pending != nil {
		st.Pending, pending = v.pending.Name, v.pending.IDs
	}
	trial := v.rep.IsTrial() && v.pending != nil && v.rep.Set == v.pending.Name
	missing := map[string]bool{}
	for _, e := range v.plan.Missing {
		missing[e.ID] = true
	}
	if v.cat != nil {
		for _, e := range v.cat.Entries {
			x := ExtensionStatus{ID: e.ID, Core: e.Core, Wanted: slices.Contains(v.wanted, e.ID),
				Mounted: v.rep.IsMounted(e.ID), Skipped: v.rep.SkipReason(e.ID), Error: s.errs[e.ID]}
			if p := s.progress[e.ID]; p != nil {
				cp := *p
				x.Progress = &cp
			}
			inPending := slices.Contains(pending, e.ID)
			if x.Error == "" && s.noVerity && missing[e.ID] {
				x.Error = noVerityText
			}
			switch {
			case x.Progress != nil:
				x.State = StateDownloading
			case slices.Contains(v.plan.Want, e.ID) && (x.Error != "" ||
				(v.plan.Blocked && slices.Contains(v.plan.IDs, e.ID) && !x.Mounted) ||
				(trial && inPending && !x.Mounted && x.Skipped != store.SkipNotInCatalog)):
				x.State = StateNeedsAttention
			case v.restart && inPending != x.Mounted:
				x.State = StateRestartNeeded
			case x.Mounted:
				x.State = StateInstalled
			default:
				x.State = StateNotInstalled
			}
			st.Extensions = append(st.Extensions, x)
		}
	}
	for _, id := range v.wanted {
		if _, ok := v.cat.Get(id); !ok {
			st.Extensions = append(st.Extensions, ExtensionStatus{ID: id, State: StateNotInThisVersion, Wanted: true,
				Mounted: v.rep.IsMounted(id), Skipped: v.rep.SkipReason(id)})
		}
	}
	return st
}
