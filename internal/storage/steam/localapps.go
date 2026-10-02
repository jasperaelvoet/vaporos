package steam

import "strconv"

// AppLaunchOptions returns the launch options of every app in an
// account's localconfig.vdf that has any, by app id, so a caller can find
// the dispatcher wherever it was put. Keys that are not app ids are
// skipped; where an app repeats, the first block counts, as for Steam.
func AppLaunchOptions(data []byte) (map[uint32]string, error) {
	root, err := parseVDF(data, LocalConfigMax)
	if err != nil {
		return nil, err
	}
	return appLaunchOptions(root), nil
}

func appLaunchOptions(root *Node) map[uint32]string {
	out := map[uint32]string{}
	apps, matched := walkVDF(root, localAppsPath)
	if matched < len(localAppsPath) {
		return out
	}
	seen := map[uint64]bool{}
	for _, a := range apps.Children {
		id, err := strconv.ParseUint(a.Key, 10, 32)
		if err != nil || id == 0 || !a.Block || !a.applies() || seen[id] {
			continue
		}
		seen[id] = true
		if c := a.Child("LaunchOptions"); c != nil && !c.Block {
			out[uint32(id)] = c.Value
		}
	}
	return out
}

// DropEmptyApp removes app's block from localconfig.vdf when nothing is
// left in it, as when the launch options added for an app the account
// never ran are taken out again.
func DropEmptyApp(data []byte, app uint32) ([]byte, bool, error) {
	e, err := newEditor(data, LocalConfigMax)
	if err != nil {
		return nil, false, err
	}
	e.dropEmpty(localAppPath(app))
	return e.result()
}

// LocalConfig is an account's localconfig.vdf, parsed once for every
// launch options change made to it: a real one can be tens of MiB, and
// parsing it again for each app would not fit prepare's time.
type LocalConfig struct{ e *vdfEditor }

// ParseLocalConfig parses localconfig.vdf (at most LocalConfigMax bytes).
func ParseLocalConfig(data []byte) (*LocalConfig, error) {
	e, err := newEditor(data, LocalConfigMax)
	if err != nil {
		return nil, err
	}
	return &LocalConfig{e}, nil
}

// LaunchOptions is AppLaunchOptions of the file as it was parsed.
func (c *LocalConfig) LaunchOptions() map[uint32]string { return appLaunchOptions(c.e.root) }

// SetLaunchOptions sets an app's launch options, adding its block when
// it has none.
func (c *LocalConfig) SetLaunchOptions(app uint32, opts string) error {
	return c.e.set(localAppPath(app), "LaunchOptions", opts)
}

// DeleteLaunchOptions removes an app's launch options, and its block
// when nothing else is left in it.
func (c *LocalConfig) DeleteLaunchOptions(app uint32) error {
	if err := c.e.del(localAppPath(app), "LaunchOptions"); err != nil {
		return err
	}
	c.e.dropEmpty(localAppPath(app))
	return nil
}

// Bytes makes the changes in one pass: the new file, whether it differs,
// and an error when it would not parse.
func (c *LocalConfig) Bytes() ([]byte, bool, error) { return c.e.result() }
