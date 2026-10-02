package steamprep

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// appToken is what the dispatcher puts before an app's %command%, less
// the app id.
const appToken = steam.Dispatcher + " --app "

// decideLaunch returns the launch options an account should have for app
// (keep false: none at all) and VaporOS's record of them (nil: none).
// Wrapped, the user's own options stay around the dispatcher; when they
// change them in Steam, the new ones are theirs. Unwrapped, options
// VaporOS wrote and nobody changed go back to exactly what they were, and
// changed ones lose only the dispatcher.
func decideLaunch(app uint32, cur string, has bool, l *LaunchState, wrap bool) (string, bool, *LaunchState) {
	unchanged := l != nil && l.Wrote != "" && has && cur == l.Wrote
	if wrap {
		before := steam.UnwrapLaunchOptions(cur)
		if unchanged {
			before = l.Before
		}
		w, ok := steam.WrapLaunchOptions(cur, app)
		if !ok {
			return cur, has, &LaunchState{Before: cur, Conflict: true}
		}
		return w, true, &LaunchState{Wrote: w, Before: before}
	}
	if unchanged {
		return l.Before, l.Before != "", nil
	}
	if has && strings.Contains(cur, appToken) {
		return steam.UnwrapLaunchOptions(cur), true, nil
	}
	return cur, has, nil
}

// launchOptions is step 2: in each account's localconfig.vdf, the
// dispatcher in front of every app an extension hooks while steam.json
// says every bootable VaporOS has it, and nowhere else.
func (p *prep) launchOptions() {
	hooked := map[uint32]bool{}
	if p.want.Dispatcher && !p.o.Unwrap {
		for _, a := range p.want.Apps {
			if len(a.Hooks) > 0 && !isTool(a.App) && a.App&0x80000000 == 0 {
				hooked[a.App] = true
			}
		}
	}
	for _, acct := range p.accounts {
		if !p.step("launch options") {
			return
		}
		p.launchOptionsOf(acct, hooked)
	}
}

func (p *prep) launchOptionsOf(acct uint32, hooked map[uint32]bool) {
	path := steam.LocalConfigPath(p.root, acct)
	name := relName(p.root, path)
	f, err := readFile(path, steam.LocalConfigMax)
	if err == nil && f.missing {
		return // Steam writes it when the account signs in
	}
	var opts map[uint32]string
	if err == nil {
		opts, err = steam.AppLaunchOptions(f.data)
	}
	if err != nil {
		p.fail(name, err)
		return
	}
	ak := acctKey(acct)
	apps := map[uint32]bool{}
	for app := range hooked {
		apps[app] = true
	}
	for k, a := range p.st.Apps {
		if a.Launch[ak] != nil {
			if id, err := parseAppID(k); err == nil {
				apps[id] = true
			}
		}
	}
	for app, o := range opts {
		if strings.Contains(o, appToken) {
			apps[app] = true
		}
	}
	ids := make([]uint32, 0, len(apps))
	for id := range apps {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	data := f.data
	next := map[uint32]*LaunchState{}
	for _, app := range ids {
		cur, has := opts[app]
		var l *LaunchState
		if a := p.st.peekApp(app); a != nil {
			l = a.Launch[ak]
		}
		o, keep, nl := decideLaunch(app, cur, has, l, hooked[app])
		next[app] = nl
		if nl != nil && nl.Conflict && (l == nil || !l.Conflict) {
			p.o.Log.Printf("prepare: %s: app %d's launch options have several %%command%%; left as they are", name, app)
		}
		if o == cur && keep == has {
			continue
		}
		if keep {
			data, _, err = steam.SetLaunchOptions(data, app, o)
		} else if data, _, err = steam.DeleteLaunchOptions(data, app); err == nil {
			data, _, err = steam.DropEmptyApp(data, app)
		}
		if err != nil {
			p.fail(name, err)
			return
		}
		p.o.Log.Printf("prepare: %s: launch options of app %d: %q", name, app, o)
	}
	if !bytes.Equal(data, f.data) {
		if !p.step("writing " + name) {
			return
		}
		if err := f.write(data); err != nil {
			p.fail(name, err)
			return
		}
	}
	for app, nl := range next {
		if nl != nil {
			a := p.st.app(app)
			if a.Launch == nil {
				a.Launch = map[string]*LaunchState{}
			}
			a.Launch[ak] = nl
		} else if a := p.st.peekApp(app); a != nil {
			delete(a.Launch, ak)
		}
	}
	p.commit()
}

func parseAppID(s string) (uint32, error) {
	n, err := strconv.ParseUint(s, 10, 32)
	if err == nil && n == 0 {
		err = strconv.ErrRange
	}
	return uint32(n), err
}

// relName names one of Steam's files in logs and errors, relative to
// Steam's directory.
func relName(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}

// dirExists reports whether dir is a directory (not a symlink to one).
func dirExists(dir string) bool {
	fi, err := os.Lstat(dir)
	return err == nil && fi.IsDir()
}
