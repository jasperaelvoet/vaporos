package steam

import (
	"path/filepath"
	"strconv"
	"strings"
)

// Where Steam keeps the files below, under its installation (Root):
// config.vdf and loginusers.vdf for the machine, the rest per account.
func ConfigVDFPath(root string) string  { return filepath.Join(root, "config", "config.vdf") }
func LoginUsersPath(root string) string { return filepath.Join(root, "config", "loginusers.vdf") }
func LocalConfigPath(root string, account uint32) string {
	return filepath.Join(root, "userdata", appKey(account), "config", "localconfig.vdf")
}
func ShortcutsPath(root string, account uint32) string {
	return filepath.Join(root, "userdata", appKey(account), "config", "shortcuts.vdf")
}

// Where the settings VaporOS changes live. Older files spell some keys
// differently ("valve", "Apps", "Priority"); the lookups match any case.
var (
	compatToolMappingPath = []string{"InstallConfigStore", "Software", "Valve", "Steam", "CompatToolMapping"}
	localAppsPath         = []string{"UserLocalConfigStore", "Software", "Valve", "Steam", "apps"}
	userConfigPath        = []string{"AppState", "UserConfig"}
)

// CompatTool is one entry of config.vdf's CompatToolMapping: the
// compatibility tool Steam runs an app with. App 0 is the default for
// every Windows game; Steam gives it priority 75 and a choice made for
// one app 250.
type CompatTool struct {
	Name     string `json:"name"`
	Config   string `json:"config"`
	Priority string `json:"priority"`
}

// CompatToolMapping returns app's entry in config.vdf (app 0: the
// default), and false when it has none. A shortcut's entry is keyed by
// its unsigned app id.
func CompatToolMapping(data []byte, app uint32) (CompatTool, bool, error) {
	root, err := parseVDF(data, VDFMax)
	if err != nil {
		return CompatTool{}, false, err
	}
	n, matched := walkVDF(root, compatToolMappingPath)
	if matched < len(compatToolMappingPath) {
		return CompatTool{}, false, nil
	}
	e := n.Child(appKey(app))
	if e == nil || !e.Block {
		return CompatTool{}, false, nil
	}
	return CompatTool{Name: e.Str("name"), Config: e.Str("config"), Priority: e.Str("priority")}, true, nil
}

// SetCompatToolMapping writes app's entry, keeping the keys of an entry
// that is there already (and any it has besides these three).
func SetCompatToolMapping(data []byte, app uint32, t CompatTool) ([]byte, bool, error) {
	return setVDF(data, VDFMax, under(compatToolMappingPath, appKey(app)), [][2]string{{"name", t.Name}, {"config", t.Config}, {"priority", t.Priority}})
}

// DeleteCompatToolMapping removes app's entry.
func DeleteCompatToolMapping(data []byte, app uint32) ([]byte, bool, error) {
	return DeleteVDF(data, VDFMax, compatToolMappingPath, appKey(app))
}

// LaunchOptions returns an app's launch options from an account's
// localconfig.vdf, and false when it has none.
func LaunchOptions(data []byte, app uint32) (string, bool, error) {
	return GetVDF(data, LocalConfigMax, localAppPath(app), "LaunchOptions")
}

// SetLaunchOptions writes an app's launch options into localconfig.vdf.
func SetLaunchOptions(data []byte, app uint32, opts string) ([]byte, bool, error) {
	return SetVDF(data, LocalConfigMax, localAppPath(app), "LaunchOptions", opts)
}

// DeleteLaunchOptions removes an app's launch options, which Steam reads
// as none at all.
func DeleteLaunchOptions(data []byte, app uint32) ([]byte, bool, error) {
	return DeleteVDF(data, LocalConfigMax, localAppPath(app), "LaunchOptions")
}

// BetaKey returns the branch an appmanifest asks Steam for (UserConfig's
// BetaKey), and false when it asks for none (the public branch).
// MountedConfig, the branch that is installed, is Steam's own.
func BetaKey(data []byte) (string, bool, error) {
	return GetVDF(data, VDFMax, userConfigPath, "BetaKey")
}

// SetBetaKey asks Steam for branch beta in an appmanifest; "" removes the
// key, which asks for the public branch. Steam acts on it the next time
// it starts.
func SetBetaKey(data []byte, beta string) ([]byte, bool, error) {
	if beta == "" {
		return DeleteVDF(data, VDFMax, userConfigPath, "BetaKey")
	}
	return SetVDF(data, VDFMax, userConfigPath, "BetaKey", beta)
}

// Account is one Steam account that signed in on this machine
// (loginusers.vdf).
type Account struct {
	SteamID64   uint64
	AccountID   uint32 // the low 32 bits, which name its userdata/<id> directory
	AccountName string
	MostRecent  bool
}

// Accounts returns the accounts in loginusers.vdf, in file order. Only
// individual accounts count: never id 0 or an anonymous one.
func Accounts(data []byte) ([]Account, error) {
	root, err := ParseVDF(data)
	if err != nil {
		return nil, err
	}
	users := root.Child("users")
	if users == nil || !users.Block {
		return nil, nil
	}
	seen := map[uint32]bool{}
	var out []Account
	for _, u := range users.Children {
		id, err := strconv.ParseUint(u.Key, 10, 64)
		const individual = 1 // the account type in bits 52-55
		if err != nil || !u.Block || id>>52&0xf != individual || uint32(id) == 0 || seen[uint32(id)] ||
			strings.EqualFold(u.Str("AccountName"), "anonymous") {
			continue
		}
		seen[uint32(id)] = true
		out = append(out, Account{
			SteamID64:   id,
			AccountID:   uint32(id),
			AccountName: u.Str("AccountName"),
			MostRecent:  u.Str("MostRecent") == "1",
		})
	}
	return out, nil
}

func localAppPath(app uint32) []string { return under(localAppsPath, appKey(app)) }

// under returns path/key without touching path's backing array.
func under(path []string, key string) []string { return append(path[:len(path):len(path)], key) }

func appKey(app uint32) string { return strconv.FormatUint(uint64(app), 10) }
