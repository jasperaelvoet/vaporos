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
	out := map[uint32]string{}
	apps, matched := walkVDF(root, localAppsPath)
	if matched < len(localAppsPath) {
		return out, nil
	}
	seen := map[uint64]bool{}
	for _, a := range apps.Children {
		id, err := strconv.ParseUint(a.Key, 10, 32)
		if err != nil || id == 0 || !a.Block || seen[id] {
			continue
		}
		seen[id] = true
		if c := a.Child("LaunchOptions"); c != nil && !c.Block {
			out[uint32(id)] = c.Value
		}
	}
	return out, nil
}

// DropEmptyApp removes app's block from localconfig.vdf when nothing is
// left in it, as when the launch options added for an app the account
// never ran are taken out again.
func DropEmptyApp(data []byte, app uint32) ([]byte, bool, error) {
	root, err := parseVDF(data, LocalConfigMax)
	if err != nil {
		return nil, false, err
	}
	apps, matched := walkVDF(root, localAppsPath)
	if matched < len(localAppsPath) {
		return data, false, nil
	}
	c := apps.Child(appKey(app))
	if c == nil || !c.Block || len(c.Children) > 0 {
		return data, false, nil
	}
	return applySplices(data, []splice{removal(data, c)}), true, nil
}
