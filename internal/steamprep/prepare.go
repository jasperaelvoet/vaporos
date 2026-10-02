package steamprep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
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
	errs      []string
	cut       bool // ran out of time

	// shortcutsUnread: an account's shortcuts.vdf could not be read, so
	// which VaporOS shortcuts it has is not known.
	shortcutsUnread bool
}

func prepare(ctx context.Context, o Options) {
	if geteuid() == 0 {
		o.Log.Print("prepare: runs as the gaming user, never as root; nothing done")
		return
	}
	root, err := steamRoot(o.Home)
	if err != nil {
		o.Log.Printf("prepare: %v; Steam's files are left alone", err)
		return
	}
	unlock, err := steamlock.Lock(ctx)
	if err != nil {
		o.Log.Printf("prepare: the Steam lock: %v; Steam's files are left alone", err)
		return
	}
	defer unlock()
	if steamRunning(o.ProcDir, getuid()) {
		o.Log.Print("prepare: Steam is running; its files are left alone")
		return
	}
	p := &prep{ctx: ctx, o: o, root: root, statePath: StatePath(o.Home), tools: map[string]bool{}}
	if p.st, err = loadState(p.statePath); err != nil {
		o.Log.Printf("prepare: %v; starting a new record", err)
	}

	raw, err := readFile(config.ExtSteamPath(), maxDesired)
	switch {
	case err != nil:
		p.finish(fmt.Errorf("steam.json: %w", err))
		return
	case raw.missing && !o.Unwrap:
		o.Log.Print("prepare: no steam.json yet; nothing to do")
		return
	case !raw.missing:
		if p.want, err = parseDesired(raw.data, o.Log.Printf); err != nil {
			p.finish(err)
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
			o.Log.Printf("prepare: steam.json is for set %q, not this boot's; waiting for vosd to write it", p.want.Set)
			return
		}
	}

	fp := p.fingerprint(raw.data)
	if fp == p.st.Fingerprint && p.st.Error == "" {
		o.Log.Print("prepare: nothing changed since the last run")
		return
	}
	p.st.Fingerprint, p.st.Vos = "", config.BinaryVersion
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

// loadAccounts reads the accounts that signed in (loginusers.vdf).
func (p *prep) loadAccounts() {
	p.st.Accounts = []string{}
	f, err := readFile(steam.LoginUsersPath(p.root), steam.VDFMax)
	if err == nil && f.missing {
		return
	}
	var list []steam.Account
	if err == nil {
		list, err = steam.Accounts(f.data)
	}
	if err != nil {
		p.fail("loginusers.vdf", err)
		return
	}
	for _, a := range list {
		p.accounts = append(p.accounts, a.AccountID)
		p.st.Accounts = append(p.st.Accounts, acctKey(a.AccountID))
	}
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
	fmt.Fprintf(h, "vos %q unwrap %v\nsteam.json %d\n", config.BinaryVersion, p.o.Unwrap, len(desired))
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
	if apps := p.betaApps(); len(apps) > 0 {
		libs := steam.Libraries(p.root)
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
// tool: VaporOS never maps or wraps those.
func isTool(app uint32) bool { return steam.App{ID: int(app)}.IsTool() }
