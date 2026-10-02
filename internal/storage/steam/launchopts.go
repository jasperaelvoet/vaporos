package steam

import (
	"regexp"
	"strconv"
	"strings"
)

// Dispatcher is the command VaporOS puts in front of Steam's %command% so
// the mounted extensions' launch hooks run (`vos ext launch`).
const Dispatcher = "/usr/bin/vos ext launch"

// Command is the placeholder Steam replaces with the game's command line.
const Command = "%command%"

// WrapLaunchOptions returns an app's launch options with the dispatcher
// in front of %command%, as the contract's table has it: options without
// %command% follow "<dispatcher> --app N %command% ", and with one the
// dispatcher goes just before it, replacing one that is there already
// (for this app or another). Options with several %command% are left
// alone and reported with false, as is app 0.
func WrapLaunchOptions(opts string, app uint32) (string, bool) {
	if app == 0 {
		return opts, false
	}
	token := Dispatcher + " --app " + strconv.FormatUint(uint64(app), 10) + " "
	switch strings.Count(opts, Command) {
	case 0:
		if strings.TrimSpace(opts) == "" {
			return token + Command, true
		}
		return token + Command + " " + opts, true
	case 1:
		prefix, suffix, _ := strings.Cut(opts, Command)
		return trimAppTokens(prefix) + token + Command + suffix, true
	}
	return opts, false
}

// UnwrapLaunchOptions removes the dispatcher token in front of a single
// %command% and nothing else, so options the dispatcher was put in front
// of come back as "%command% <options>", which Steam runs the same way.
func UnwrapLaunchOptions(opts string) string {
	if strings.Count(opts, Command) != 1 {
		return opts
	}
	prefix, suffix, _ := strings.Cut(opts, Command)
	return trimAppTokens(prefix) + Command + suffix
}

// trimAppTokens removes "<dispatcher> --app N " tokens that end prefix.
func trimAppTokens(prefix string) string {
	for {
		rest, ok := strings.CutSuffix(prefix, " ")
		if !ok {
			return prefix
		}
		i := len(rest)
		for i > 0 && rest[i-1] >= '0' && rest[i-1] <= '9' {
			i--
		}
		head, ok := strings.CutSuffix(rest[:i], Dispatcher+" --app ")
		if i == len(rest) || !ok {
			return prefix
		}
		prefix = head
	}
}

// ShortcutLaunchOptions is the launch options of an extension's shortcut:
// the dispatcher with the shortcut's id, which is how its entry is found
// again in shortcuts.vdf whatever app id Steam keeps for it.
func ShortcutLaunchOptions(owner, key string) string {
	return Dispatcher + " --shortcut " + owner + "/" + key + " " + Command
}

// ShortcutRef returns the extension and shortcut key a shortcut's launch
// options name, and false when they name none.
func ShortcutRef(opts string) (owner, key string, ok bool) {
	_, rest, found := strings.Cut(opts, Dispatcher+" --shortcut ")
	if !found {
		return "", "", false
	}
	ref, _, _ := strings.Cut(rest, " ")
	owner, key, ok = strings.Cut(ref, "/")
	if !ok || !shortcutNameRe.MatchString(owner) || !shortcutNameRe.MatchString(key) {
		return "", "", false
	}
	return owner, key, true
}

// shortcutNameRe is an extension id or a descriptor's shortcut key.
var shortcutNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
