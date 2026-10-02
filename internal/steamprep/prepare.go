package steamprep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/steamlock"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// prep is one run.
type prep struct {
	ctx       context.Context
	o         Options
	root      string // Steam's directory
	statePath string
	st        *State
	want      *Desired // empty with --unwrap and no steam.json
	accounts  []uint32
	plans     []*shortcutPlan
	tools     map[string]bool
	toolApps  map[uint32]bool // isTool's answers
	libs      []string        // Steam's libraries, once read
	errs      []string
	cut       bool     // ran out of time
	changed   []string // Steam's files the run wrote or removed

	// shortcutsUnread: which VaporOS shortcuts an account has is not
	// known (its shortcuts.vdf or loginusers.vdf could not be read, or the
	// record has an account this run has no plan for), so none of their
	// mappings goes.
	shortcutsUnread bool
	// keptShortcuts are the app ids of VaporOS shortcuts kept although
	// steam.json does not list them this run.
	keptShortcuts map[uint32]bool
	// extOff: the boot report's mode is off (vos.ext=0, skip-once, no
	// report), a boot without extensions that says nothing about which
	// ones stay: no shortcut goes.
	extOff bool
	// libsPartial: a libraryfolders.vdf could not be read or parsed, so
	// libs may lack the library an app is in.
	libsPartial bool
}

// prepare's last line says what it did ("prepare: done…") or why it
// changed nothing ("prepare: skipped: …"): vosd logs it when it runs
// prepare itself, and the journal has it after every Steam start and stop.
func prepare(ctx context.Context, o Options) {
	if geteuid() == 0 {
		o.Log.Print("prepare: skipped: it runs as the gaming user, never as root; nothing done")
		return
	}
	root, err := steamRoot(o.Home)
	if err != nil {
		o.Log.Printf("prepare: skipped: %v; Steam's files are left alone", err)
		return
	}
	// Without the lock another run may be writing the record, so this
	// bail-out alone records nothing.
	unlock, err := steamlock.Lock(ctx)
	if err != nil {
		o.Log.Printf("prepare: skipped: %s; Steam's files are left alone", lockTrouble(ctx, err))
		return
	}
	defer unlock()
	p := &prep{ctx: ctx, o: o, root: root, statePath: StatePath(o.Home), tools: map[string]bool{},
		toolApps: map[uint32]bool{}, keptShortcuts: map[uint32]bool{}}
	if p.st, err = loadState(p.statePath); err != nil {
		o.Log.Printf("prepare: %v; starting a new record", err)
	}
	if steamRunning(o.ProcDir, getuid()) {
		p.skip(skipSteamRunning, "Steam is running; its files are left alone")
		return
	}

	raw, err := readFile(config.ExtSteamPath(), maxDesired)
	switch {
	case err != nil:
		p.fail("prepare", fmt.Errorf("steam.json: %w", err))
		p.skip(skipBadDesired, "steam.json cannot be read; Steam's files are left alone")
		return
	case raw.missing && !o.Unwrap:
		p.skip(skipNoDesired, "no steam.json yet; nothing to do")
		return
	case !raw.missing:
		if p.want, err = parseDesired(raw.data, o.Log.Printf); err != nil {
			p.fail("prepare", err)
			p.skip(skipBadDesired, "steam.json does not parse; Steam's files are left alone")
			return
		}
	default:
		p.want = &Desired{}
	}
	if !o.Unwrap {
		rep, err := store.LoadBootReport()
		if err != nil {
			o.Log.Printf("prepare: %v", err)
		}
		if rep == nil || rep.Set != p.want.Set {
			p.skip(skipOtherSet, fmt.Sprintf("steam.json is for set %q, not this boot's; waiting for vosd to write it", p.want.Set))
			return
		}
		p.extOff = rep.Mode == store.ModeOff
	}

	fp := p.fingerprint(raw.data)
	if fp == p.st.Fingerprint && p.st.Error == "" {
		o.Log.Print("prepare: done; nothing changed since the last run")
		if p.st.Skipped != "" {
			p.st.Skipped = ""
			p.commit()
		}
		return
	}
	p.st.Fingerprint, p.st.Vos, p.st.Skipped = "", config.BinaryVersion, ""
	p.loadAccounts()
	for _, step := range []struct {
		name string
		run  func()
	}{
		{"shortcuts plan", p.planShortcuts},
		{"compatibility tools", p.compatTools},
		{"launch options", p.launchOptions},
		{"shortcuts", p.writeShortcuts},
		{"art", p.art},
		{"branches", p.branches},
	} {
		if !p.step(step.name) {
			break
		}
		step.run()
	}
	if len(p.errs) == 0 && !p.cut {
		fp = p.fingerprint(raw.data)
	} else {
		fp = ""
	}
	p.st.Fingerprint = fp
	p.finish(nil)
	o.Log.Print(p.summary())
}

// lockTrouble says why the Steam lock could not be taken.
func lockTrouble(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "no runtime directory " + config.GamerRuntimeDir + " for the Steam lock (the gaming user has no session)"
	case ctx.Err() != nil:
		return "the Steam lock stayed busy (another prepare, or vosd adding a library)"
	}
	return "the Steam lock: " + err.Error()
}

// wrote notes one of Steam's files the run replaced or removed.
func (p *prep) wrote(path string) { p.changed = append(p.changed, relName(p.root, path)) }

// summary is a full run's last line.
func (p *prep) summary() string {
	what := "nothing needed changing"
	if n := len(p.changed); n > 0 {
		names := p.changed[:min(n, 4)]
		what = "changed " + strings.Join(names, ", ")
		if n > len(names) {
			what += fmt.Sprintf(" and %d more", n-len(names))
		}
	}
	mode := ""
	switch {
	case p.o.Unwrap:
		mode = " (--unwrap)"
	case !p.want.Dispatcher:
		mode = " (dispatcher off)"
	}
	switch n := len(p.errs); {
	case p.cut:
		return "prepare: stopped, out of time" + mode + "; " + what + "; the next run goes on"
	case n == 1:
		return "prepare: done" + mode + " with 1 error (above); " + what
	case n > 1:
		return fmt.Sprintf("prepare: done%s with %d errors (above); %s", mode, n, what)
	}
	return "prepare: done" + mode + "; " + what
}

// step reports whether there is time for the next step.
func (p *prep) step(name string) bool {
	if p.cut {
		return false
	}
	if stepHook != nil {
		stepHook(p.ctx, name)
	}
	if p.ctx.Err() != nil {
		p.cut = true
		p.fail("out of time before "+name, p.ctx.Err())
		return false
	}
	return true
}

// fail records a problem with one file or step; the run goes on with the
// others.
func (p *prep) fail(what string, err error) {
	msg := what + ": " + err.Error()
	p.o.Log.Printf("prepare: %s", msg)
	p.errs = append(p.errs, msg)
}

// commit saves the record, right after each of Steam's files is replaced.
func (p *prep) commit() {
	if err := p.st.save(p.statePath); err != nil {
		p.o.Log.Printf("prepare: saving %s: %v", p.statePath, err)
	}
}

func (p *prep) finish(err error) {
	if err != nil {
		p.fail("prepare", err)
	}
	p.st.Error = strings.Join(p.errs, "; ")
	p.commit()
}

// steamRoot returns Steam's directory, ~/.local/share/Steam, which is
// where vosd looks. Steam keeps ~/.steam/root pointing at it; one that
// points elsewhere means a Steam vosd does not see, which is left alone.
func steamRoot(home string) (string, error) {
	root := filepath.Join(home, ".local", "share", "Steam")
	link := filepath.Join(home, ".steam", "root")
	if _, err := os.Lstat(link); errors.Is(err, os.ErrNotExist) {
		return root, nil // Steam never ran
	}
	got, err := filepath.EvalSymlinks(link)
	if err != nil {
		return "", fmt.Errorf("~/.steam/root: %w", err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil || got != want {
		return "", fmt.Errorf("~/.steam/root is %s, not ~/.local/share/Steam", got)
	}
	return root, nil
}

// skip ends a run that changes nothing. It records why, and the accounts
// that signed in: vosd asks for a Steam restart when Steam has one the
// record lacks, and a run that skips must not make it ask again.
func (p *prep) skip(why, msg string) {
	p.o.Log.Printf("prepare: skipped: %s", msg)
	p.st.Skipped = why
	if ids, err := p.readAccounts(); err == nil {
		p.st.Accounts = acctKeys(ids)
	}
	if len(p.errs) > 0 {
		p.st.Error = strings.Join(p.errs, "; ")
	}
	p.commit()
}

// readAccounts returns the accounts in loginusers.vdf, none while it is
// missing.
func (p *prep) readAccounts() ([]uint32, error) {
	f, err := readFile(steam.LoginUsersPath(p.root), steam.VDFMax)
	if err != nil || f.missing {
		return nil, err
	}
	list, err := steam.Accounts(f.data)
	if err != nil {
		return nil, err
	}
	ids := make([]uint32, 0, len(list))
	for _, a := range list {
		ids = append(ids, a.AccountID)
	}
	return ids, nil
}

// loadAccounts reads the accounts that signed in. Without them, which
// shortcuts VaporOS has is not known either, and the record keeps the
// accounts it has: vosd would take an empty list for new accounts and
// restart Steam for them.
func (p *prep) loadAccounts() {
	ids, err := p.readAccounts()
	if err != nil {
		p.fail("loginusers.vdf", err)
		p.shortcutsUnread = true
		return
	}
	p.accounts = ids
	p.st.Accounts = acctKeys(ids)
}

func acctKeys(ids []uint32) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, acctKey(id))
	}
	return out
}

// toolOK reports whether compatibility tool name is installed. Steam
// keeps a mapping to a missing tool, and a game mapped to one does not
// start, so VaporOS maps nothing to it.
func (p *prep) toolOK(name string) bool {
	if name == "" {
		return false
	}
	ok, seen := p.tools[name]
	if !seen {
		fi, err := os.Stat(filepath.Join(config.CompatToolsDir, name, "compatibilitytool.vdf"))
		ok = err == nil && fi.Mode().IsRegular()
		p.tools[name] = ok
	}
	return ok
}

// fingerprint covers everything a run depends on, so an unchanged one
// means there is nothing to do: steam.json, this vos, --unwrap, which
// tools are installed, who signed in, and the size and time of every file
// of Steam's a run reads.
func (p *prep) fingerprint(desired []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "vos %q unwrap %v off %v\nsteam.json %d\n", config.BinaryVersion, p.o.Unwrap, p.extOff, len(desired))
	h.Write(desired)

	tools := []string{p.st.Default.Wrote}
	for _, a := range p.st.Apps {
		if a.Mapping != nil {
			tools = append(tools, a.Mapping.Wrote)
		}
	}
	if p.want != nil {
		tools = append(tools, p.want.DefaultCompatTool)
		for _, a := range p.want.Apps {
			tools = append(tools, a.CompatTool)
		}
		for _, s := range p.want.Shortcuts {
			tools = append(tools, s.CompatTool)
		}
	}
	slices.Sort(tools)
	for _, t := range slices.Compact(tools) {
		if t != "" {
			fmt.Fprintf(h, "tool %q %v\n", t, p.toolOK(t))
		}
	}

	users, _ := readFile(steam.LoginUsersPath(p.root), steam.VDFMax)
	if users != nil {
		fmt.Fprintf(h, "loginusers %v %d\n", users.missing, len(users.data))
		h.Write(users.data)
		if list, err := steam.Accounts(users.data); err == nil {
			for _, a := range list {
				statLine(h, steam.LocalConfigPath(p.root, a.AccountID))
				statLine(h, steam.ShortcutsPath(p.root, a.AccountID))
			}
		}
	}
	statLine(h, steam.ConfigVDFPath(p.root))
	statLine(h, filepath.Join(p.root, "steamapps", "libraryfolders.vdf"))
	statLine(h, filepath.Join(p.root, "config", "libraryfolders.vdf"))
	if apps := p.betaApps(); len(apps) > 0 {
		libs := p.libraries()
		for _, lib := range libs {
			fmt.Fprintf(h, "library %q %v\n", lib, dirExists(filepath.Join(lib, "steamapps")))
		}
		for _, app := range apps {
			for _, lib := range libs {
				statLine(h, manifestPath(lib, app))
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func statLine(h hash.Hash, path string) {
	fi, err := os.Lstat(path)
	if err != nil {
		fmt.Fprintf(h, "file %q -\n", path)
		return
	}
	fmt.Fprintf(h, "file %q %d %d %v\n", path, fi.Size(), fi.ModTime().UnixNano(), fi.Mode())
}

func acctKey(id uint32) string { return strconv.FormatUint(uint64(id), 10) }

// isTool reports whether app is Proton, a Steam Linux Runtime or another
// of Steam's tools, by its id or by the name in its installed
// appmanifest: VaporOS never maps, wraps or switches those.
func (p *prep) isTool(app uint32) bool {
	if (steam.App{ID: int(app)}).IsTool() {
		return true
	}
	if app&0x80000000 != 0 {
		return false // a shortcut
	}
	is, seen := p.toolApps[app]
	if !seen {
		if f, err := findManifest(p.libraries(), app); err == nil && f != nil {
			m, err := steam.ParseManifest(f.data)
			is = err == nil && m.IsTool()
		}
		p.toolApps[app] = is
	}
	return is
}

// libraries returns Steam's libraries, read once a run. A
// libraryfolders.vdf that cannot be read or parsed is the run's error.
func (p *prep) libraries() []string {
	if p.libs == nil {
		var err error
		if p.libs, err = steam.ReadLibraries(p.root); err != nil {
			p.libsPartial = true
			p.fail("libraryfolders.vdf", err)
		}
	}
	return p.libs
}
