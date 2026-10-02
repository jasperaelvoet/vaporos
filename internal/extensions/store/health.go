package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// AfterHealthy records a boot that passed `vos health`: every image it
// mounted is proven (an OS trial included), and on an extension trial the
// tried set is promoted (PromoteTrial, whatever the desired set is). The
// promotion does not depend on proven: a failure to record the images is
// returned after it. Caller holds Lock.
func AfterHealthy(rep *BootReport) error {
	if rep == nil {
		return nil
	}
	var provenErr error
	switch rep.Mode {
	case ModePending, ModeOSTrial, ModeEnabled:
		provenErr = AddProven(rep.MountedPairs())
	}
	return errors.Join(provenErr, PromoteTrial(rep, ""))
}

// PromoteTrial makes the set this boot tried (mode pending) the enabled
// one, provided pending still names it and every id of the set mounted or
// was skipped only because this catalog does not list it. A caller that
// knows the desired set passes want (Plan.PromoteWant), and the set is
// promoted only when what booted has that fingerprint, so a set the user
// has since changed is never made the fallback; an empty want skips this
// check. Caller holds Lock.
func PromoteTrial(rep *BootReport, want string) error {
	if rep == nil || rep.Mode != ModePending || rep.Set == "" {
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

// WriteTrialOK records, for this boot only (on /run), that the trial of
// set passed `vos health`. Written before the promotion under Lock, so
// vosd can still promote it when health could not.
func WriteTrialOK(set string) error {
	if !setNameRe.MatchString(set) {
		return fmt.Errorf("invalid set name %q", set)
	}
	return config.WriteFileAtomic(config.ExtTrialOKPath(), []byte(set+"\n"), 0o644)
}

// TrialOK returns the set whose trial passed `vos health` this boot, or ""
// when none did (or the marker does not hold a set name).
func TrialOK() (string, error) {
	f, err := os.Open(config.ExtTrialOKPath())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSetTriesFile))
	if err != nil {
		return "", err
	}
	if s := strings.TrimSpace(string(b)); setNameRe.MatchString(s) {
		return s, nil
	}
	return "", nil
}
