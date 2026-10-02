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
// in front of %command%, as the contract's table has it. Every dispatcher
// token for an app goes first (UnwrapLaunchOptions), wherever it is and
// whichever app it names, so wrapping is idempotent. Then options without
// %command% follow "<dispatcher> --app N %command% ", and with one the
// token goes right before it. Options with several %command% come back
// without the tokens and false, as does app 0.
func WrapLaunchOptions(opts string, app uint32) (string, bool) {
	opts = UnwrapLaunchOptions(opts)
	if app == 0 {
		return opts, false
	}
	token := appToken + strconv.FormatUint(uint64(app), 10) + " "
	switch strings.Count(opts, Command) {
	case 0:
		if strings.TrimSpace(opts) == "" {
			return token + Command, true
		}
		return token + Command + " " + opts, true
	case 1:
		prefix, suffix, _ := strings.Cut(opts, Command)
		return prefix + token + Command + suffix, true
	}
	return opts, false
}

// appToken starts the dispatcher's token for an app, less its id.
const appToken = Dispatcher + " --app "

// UnwrapLaunchOptions removes every "<dispatcher> --app N" token and the
// spaces and tabs after it, so options the dispatcher was put in front of
// come back as "%command% <options>", which Steam runs the same way. A
// token is only ours when its digits end the string, or are followed by
// a space, a tab or %command%.
func UnwrapLaunchOptions(opts string) string {
	var b strings.Builder
	for {
		i := strings.Index(opts, appToken)
		if i < 0 {
			break
		}
		digits := i + len(appToken)
		end := digits
		for end < len(opts) && opts[end] >= '0' && opts[end] <= '9' {
			end++
		}
		ws := end
		for end < len(opts) && (opts[end] == ' ' || opts[end] == '\t') {
			end++
		}
		if ws == digits || (end == ws && ws < len(opts) && !strings.HasPrefix(opts[ws:], Command)) {
			b.WriteString(opts[:digits]) // not one of ours
			opts = opts[digits:]
			continue
		}
		b.WriteString(opts[:i])
		opts = opts[end:]
	}
	b.WriteString(opts)
	return b.String()
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
