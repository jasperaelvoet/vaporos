package store

import (
	"errors"
	"io/fs"
)

// AfterHealthy records a boot that passed `vos health`: every image it
// mounted is proven (an OS trial included). On an extension trial the
// tried set becomes enabled, provided pending still names it and every id
// of the set mounted or was skipped only because this catalog does not list
// it. Caller holds Lock.
func AfterHealthy(rep *BootReport) error { return AfterHealthyWant(rep, "") }

// AfterHealthyWant is AfterHealthy for a caller that knows the desired set:
// want is its fingerprint (Plan.Fingerprint), and the tried set is promoted
// only when what booted has that fingerprint, so a set the user has since
// changed is never made the fallback. An empty want skips this check.
// Caller holds Lock.
func AfterHealthyWant(rep *BootReport, want string) error {
	if rep == nil {
		return nil
	}
	switch rep.Mode {
	case ModePending, ModeOSTrial, ModeEnabled:
		if err := AddProven(rep.MountedPairs()); err != nil {
			return err
		}
	}
	if rep.Mode != ModePending || rep.Set == "" {
		return nil
	}
	s, err := ReadSet(rep.Set)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, id := range s.IDs {
		if !rep.IsMounted(id) && rep.SkipReason(id) != SkipNotInCatalog {
			return nil
		}
	}
	if want != "" && Fingerprint(rep.MountedPairs(), s.Options) != want {
		return nil
	}
	_, err = Promote(rep.Set)
	return err
}
