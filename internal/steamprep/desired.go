package steamprep

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// Desired is /var/lib/vos/ext/steam.json: what the extensions this boot
// mounted ask of Steam. vosd writes it; prepare only reads it.
type Desired struct {
	Set               string       `json:"set"`
	Dispatcher        bool         `json:"dispatcher"`
	DefaultCompatTool string       `json:"default_compat_tool"`
	Apps              []AppWant    `json:"apps"`
	Shortcuts         []Shortcut   `json:"shortcuts"`
	Release           []AppRelease `json:"release"`
	// Owners are the extensions whose shortcuts stay while steam.json
	// lists none of theirs (wanted ∪ core ∪ mounted); nil when vosd could
	// not tell (null or missing), and then no shortcut goes.
	Owners []string `json:"owners"`

	// named are the extensions steam.json names anywhere, entries not
	// well formed included; owners the well formed ids of Owners. A
	// VaporOS shortcut goes only once its extension is in neither.
	named, owners map[string]bool
}

// AppWant is what extensions ask for one Steam app.
type AppWant struct {
	App        uint32    `json:"app"`
	CompatTool string    `json:"compat_tool"`
	Hooks      []string  `json:"hooks"`
	Beta       *BetaWant `json:"beta"`

	// keepBranch: Beta was not well formed, so the app's branch and the
	// record of it are left as they are.
	keepBranch bool
}

// BetaWant is a request for an app's branch ("" the public one). vosd
// gives every request its own id; prepare applies each one once, so a
// branch the user picks in Steam afterwards stays.
type BetaWant struct {
	Branch  string `json:"branch"`
	Request string `json:"request"`
	bad     bool
}

// UnmarshalJSON keeps a beta that is not such an object from failing all
// of steam.json: parseDesired then drops it alone.
func (b *BetaWant) UnmarshalJSON(data []byte) error {
	type plain BetaWant
	var p plain
	if json.Unmarshal(data, &p) != nil {
		*b = BetaWant{bad: true}
		return nil
	}
	*b = BetaWant(p)
	return nil
}

// Shortcut is a non-Steam game an extension adds.
type Shortcut struct {
	Owner      string   `json:"owner"`
	Key        string   `json:"key"`
	Name       string   `json:"name"`
	Exe        string   `json:"exe"`
	StartDir   string   `json:"start_dir"`
	Args       []string `json:"args,omitempty"`
	CompatTool string   `json:"compat_tool"`
	Art        string   `json:"art"`
}

// Ref is the shortcut's "<owner>/<key>".
func (s Shortcut) Ref() string { return s.Owner + "/" + s.Key }

// AppRelease is an app whose compatibility tool a removed extension used
// to force: its mapping stays, and it is the user's from now on.
type AppRelease struct {
	App uint32 `json:"app"`
}

const maxDesired = 1 << 20

var (
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)               // extension ids, shortcut keys
	toolRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)    // compatibility tools
	betaRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]{0,63})?$`) // "" is the public branch
	reqRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)    // branch request ids
	argRe  = regexp.MustCompile(`^[A-Za-z0-9._/:=+-]{1,128}$`)          // a word of a shortcut's args
)

// parseDesired reads steam.json and drops, with a log line, any entry that
// is not well formed: vosd writes it, but its values end up in paths and
// in Steam's files.
func parseDesired(data []byte, logf func(string, ...any)) (*Desired, error) {
	if len(data) > maxDesired {
		return nil, fmt.Errorf("steam.json: too large")
	}
	var d Desired
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("steam.json: %w", err)
	}
	if d.DefaultCompatTool != "" && !toolRe.MatchString(d.DefaultCompatTool) {
		logf("steam.json: default compatibility tool %q is not a tool name; ignored", d.DefaultCompatTool)
		d.DefaultCompatTool = ""
	}
	d.named, d.owners = map[string]bool{}, map[string]bool{}
	for _, a := range d.Apps {
		for _, h := range a.Hooks {
			if nameRe.MatchString(h) {
				d.named[h] = true
			}
		}
	}
	for _, s := range d.Shortcuts {
		if nameRe.MatchString(s.Owner) {
			d.named[s.Owner] = true
		}
	}
	for _, id := range d.Owners {
		if nameRe.MatchString(id) {
			d.owners[id] = true
		} else {
			logf("steam.json: owner %q is not an extension id; ignored", id)
		}
	}
	apps := d.Apps[:0]
	seen := map[uint32]bool{}
	for _, a := range d.Apps {
		switch {
		case a.App == 0 || seen[a.App]:
			logf("steam.json: app %d listed twice or 0; ignored", a.App)
			continue
		case a.CompatTool != "" && !toolRe.MatchString(a.CompatTool):
			logf("steam.json: app %d: %q is not a tool name; ignored", a.App, a.CompatTool)
			continue
		case a.Beta != nil && (a.Beta.bad || !betaRe.MatchString(a.Beta.Branch) || !requestOK(a.Beta.Request)):
			logf("steam.json: app %d: the branch asked for is not {\"branch\",\"request\"}; its branch is left as it is", a.App)
			a.Beta, a.keepBranch = nil, true
		}
		hooks := a.Hooks[:0]
		for _, h := range a.Hooks {
			if nameRe.MatchString(h) {
				hooks = append(hooks, h)
			}
		}
		a.Hooks = hooks
		seen[a.App] = true
		apps = append(apps, a)
	}
	d.Apps = apps
	shortcuts := d.Shortcuts[:0]
	refs := map[string]bool{}
	for _, s := range d.Shortcuts {
		err := checkShortcut(s)
		if err == nil && refs[s.Ref()] {
			err = fmt.Errorf("listed twice")
		}
		if err != nil {
			logf("steam.json: shortcut %q: %v; ignored", s.Ref(), err)
			continue
		}
		refs[s.Ref()] = true
		shortcuts = append(shortcuts, s)
	}
	d.Shortcuts = shortcuts
	return &d, nil
}

func checkShortcut(s Shortcut) error {
	switch {
	case !nameRe.MatchString(s.Owner) || !nameRe.MatchString(s.Key):
		return fmt.Errorf("not <extension>/<key>")
	case s.Name == "" || len(s.Name) > 128 || strings.ContainsFunc(s.Name, unicode.IsControl):
		return fmt.Errorf("bad name")
	case !cleanAbs(s.Exe) || !cleanAbs(s.StartDir):
		return fmt.Errorf("exe and start_dir must be absolute paths")
	case len(s.Args) > 16 || slices.ContainsFunc(s.Args, func(a string) bool { return !argRe.MatchString(a) }):
		return fmt.Errorf("args must be at most 16 plain words")
	case s.CompatTool != "" && !toolRe.MatchString(s.CompatTool):
		return fmt.Errorf("%q is not a tool name", s.CompatTool)
	case s.Art != "" && (!cleanAbs(s.Art) || !strings.HasPrefix(s.Art, filepath.Join(config.ExtMountedLibDir, s.Owner)+"/")):
		return fmt.Errorf("art must be in %s/%s/", config.ExtMountedLibDir, s.Owner)
	}
	return nil
}

// requestOK accepts a branch request's id: opaque, but short and plain,
// since it goes into prepare's record.
func requestOK(id string) bool { return reqRe.MatchString(id) }

// cleanAbs accepts an absolute, clean path that Steam can keep in quotes.
func cleanAbs(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && len(p) <= 1024 &&
		!strings.ContainsFunc(p, func(r rune) bool { return r == '"' || unicode.IsControl(r) })
}

// names reports whether steam.json names extension id anywhere.
func (d *Desired) names(id string) bool { return d.named[id] }

// keeps reports whether extension id's shortcuts stay though steam.json
// does not list them.
func (d *Desired) keeps(id string) bool { return d.named[id] || d.Owners == nil || d.owners[id] }

// releases returns the apps steam.json hands to the user.
func (d *Desired) releases() map[uint32]bool {
	out := map[uint32]bool{}
	for _, r := range d.Release {
		if r.App != 0 {
			out[r.App] = true
		}
	}
	return out
}
