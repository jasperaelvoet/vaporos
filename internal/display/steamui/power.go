package steamui

import (
	"context"
	"errors"
	"fmt"
	"math"
)

// ErrNoSleepAPI: Steam lacks what a sleep call needs (SuspendResumeStore's
// InitiateResume, or SteamClient.Settings.SetSetting). Scaling does not
// depend on it.
var ErrNoSleepAPI = errors.New("steamui: Steam has no suspend store or settings setter")

// Steam's ESuspendResumeProgressState (steammessages): 2 to 5 (cloud sync,
// logging in, waiting for a game, working) mean Steam still prepares the
// suspend.
const (
	SuspendInvalid  = 0
	SuspendComplete = 1
)

// SleepState is Steam's suspend state and idle sleep settings as its
// SharedJSContext holds them. A number Steam does not show, or shows
// as anything but a whole number within int32, is -1.
type SleepState struct {
	Store      bool // window.SuspendResumeStore is there
	Suspending bool // Steam's interface is in (or on its way to) its suspend screen
	Progress   int  // GetSuspendResumeState(), an ESuspendResumeProgressState
	CanResume  bool // the store has InitiateResume
	CanSet     bool // SteamClient.Settings.SetSetting is there
	AC         int  // system_idle_suspend_ac_sec, 0 for Never
	Battery    int  // system_idle_suspend_battery_sec, 0 for Never
}

// ResumeResult is what Resume found.
type ResumeResult int

const (
	Awake           ResumeResult = iota // Steam was not suspending: nothing was called
	Resumed                             // Steam left its suspend screen
	StillSuspending                     // InitiateResume ran, and Steam still suspends
)

func (r ResumeResult) String() string {
	switch r {
	case Awake:
		return "awake"
	case Resumed:
		return "resumed"
	case StillSuspending:
		return "suspending"
	}
	return fmt.Sprintf("ResumeResult(%d)", int(r))
}

// Sleep reads Steam's suspend state and idle sleep settings.
func (c *Client) Sleep(ctx context.Context) (SleepState, error) {
	var r sleepResult
	if err := c.evalShared(ctx, sleepJS, &r); err != nil {
		return SleepState{}, err
	}
	return r.decode(), nil
}

// NeverSleep sets Steam's idle sleep, on AC and on battery, to Never, as
// Steam's own setting does, without waiting for Steam to store it.
// ErrNoSleepAPI: Steam lacks SetSetting.
func (c *Client) NeverSleep(ctx context.Context) error {
	var r statusResult
	if err := c.evalShared(ctx, neverSleepJS, &r); err != nil {
		return err
	}
	return r.neverSleep()
}

// Resume takes Steam's interface out of its suspend screen, as Steam does
// when the system resumes; it calls nothing when Steam is not suspending.
// ErrNoSleepAPI: the store has no InitiateResume.
func (c *Client) Resume(ctx context.Context) (ResumeResult, error) {
	var r statusResult
	if err := c.evalShared(ctx, resumeJS, &r); err != nil {
		return 0, err
	}
	return r.resume()
}

// sleepResult is what sleepJS returns.
type sleepResult struct {
	Store      bool     `json:"store"`
	Suspending bool     `json:"suspending"`
	Progress   *float64 `json:"progress"`
	Resume     bool     `json:"resume"`
	Set        bool     `json:"set"`
	AC         *float64 `json:"ac"`
	Battery    *float64 `json:"battery"`
}

func (r sleepResult) decode() SleepState {
	return SleepState{
		Store:      r.Store,
		Suspending: r.Suspending,
		Progress:   count(r.Progress),
		CanResume:  r.Resume,
		CanSet:     r.Set,
		AC:         count(r.AC),
		Battery:    count(r.Battery),
	}
}

// statusResult is what neverSleepJS and resumeJS return.
type statusResult struct {
	Status string `json:"status"`
}

func (r statusResult) neverSleep() error {
	switch r.Status {
	case "ok":
		return nil
	case "unsupported":
		return ErrNoSleepAPI
	}
	return r.unexpected()
}

func (r statusResult) resume() (ResumeResult, error) {
	switch r.Status {
	case "resumed":
		return Resumed, nil
	case "suspending":
		return StillSuspending, nil
	case "awake":
		return Awake, nil
	case "unsupported":
		return 0, ErrNoSleepAPI
	}
	return 0, r.unexpected()
}

func (r statusResult) unexpected() error {
	return fmt.Errorf("steamui: unexpected status %q", clampString(r.Status, 32))
}

// count is *p as a whole number within int32, -1 for anything else: an
// unknown setting must never read as 0, which is Never.
func count(p *float64) int {
	if p == nil || !(*p >= 0 && *p <= math.MaxInt32) || *p != math.Trunc(*p) {
		return -1
	}
	return int(*p)
}
