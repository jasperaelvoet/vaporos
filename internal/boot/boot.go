// Package boot manages the ESP: systemd-boot entries per slot with boot
// counting, kernels under /vos/<ver>/, loader.conf, and the kernel command
// line (vos.slot + image cmdline + machine cmdline). Ported from the bash vos.
//
// Entries are named vos-<ver>[+LEFT[-DONE]].conf. systemd-boot decrements
// the counter on every attempt, systemd-bless-boot drops it once
// boot-complete.target is reached, and an entry at +0 sorts last. A new
// slot that never becomes healthy therefore falls back to the old one on
// its own.
//
// The ESP is FAT. chmod fails there with EPERM under our fmask, so nothing
// here sets modes, and every write goes to a temp file renamed into place.
package boot

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// DefaultTries is how many boot attempts a freshly written slot gets.
const DefaultTries = 3

// LoaderConf is loader/loader.conf (docs/CONTRACTS.md): boot the default
// entry at once, no editor, and no menu entries we did not write.
const LoaderConf = "timeout 0\neditor no\nauto-entries no\nauto-firmware no\nconsole-mode keep\n"

// BootFiles are the files InstallEntry copies from its source directory
// into /vos/<ver>/ on the ESP.
var BootFiles = []string{"vmlinuz", "initramfs.img"}

const (
	entryPrefix = "vos-"
	entrySuffix = ".conf"
	// Retries when systemd-bless-boot renames an entry under us.
	raceRetries = 5
)

// beforeRename runs between the last check of an entry's name and the
// rename onto it. Tests use it to rename the entry the way
// systemd-bless-boot would.
var beforeRename = func() {}

type Entry struct {
	Path     string // full path of the .conf
	Version  string
	Slot     string
	Counting bool // has +N[-M]
	Left     int  // tries left (N)
	Done     int  // tries done (M)
	Options  string
	Linux    string // the linux line, e.g. /vos/<ver>/vmlinuz
	Initrd   string // the initrd line
}

// Bootable reports whether systemd-boot still prefers this entry: it has no
// boot counter, or it has tries left.
func (e Entry) Bootable() bool { return !e.Counting || e.Left > 0 }

// Name is the entry's file name.
func (e Entry) Name() string { return filepath.Base(e.Path) }

// Base is the file name without the boot counter and suffix ("vos-<ver>").
func (e Entry) Base() string {
	base, _, _, _ := splitName(e.Name())
	return base
}

func entriesDir(esp string) string { return filepath.Join(esp, "loader", "entries") }

// Cmdline composes an entry's options line: vos.slot first, then the image's
// arguments, then the machine's. Stray vos.slot arguments and exact
// duplicates are dropped, so composing twice gives the same line.
func Cmdline(slot, imageCmdline, machineCmdline string) string {
	var out []string
	if slot != "" {
		out = append(out, "vos.slot="+slot)
	}
	seen := map[string]bool{}
	for _, a := range append(SplitArgs(imageCmdline), SplitArgs(machineCmdline)...) {
		if a == "vos.slot" || strings.HasPrefix(a, "vos.slot=") || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return strings.Join(out, " ")
}

// SplitArgs splits a kernel command line into arguments. It keeps
// double-quoted values (foo="a b") in one piece, as the kernel does.
func SplitArgs(s string) []string {
	var args []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case !quoted && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

// SwapMachineArgs replaces the machine arguments oldMachine in an entry's
// options with newMachine, and leaves vos.slot and the image's arguments
// alone.
func SwapMachineArgs(options, oldMachine, newMachine string) string {
	drop := map[string]bool{}
	for _, a := range SplitArgs(oldMachine) {
		drop[a] = true
	}
	slot := ""
	var keep []string
	for _, a := range SplitArgs(options) {
		if v, ok := strings.CutPrefix(a, "vos.slot="); ok {
			slot = v
			continue
		}
		if !drop[a] {
			keep = append(keep, a)
		}
	}
	return Cmdline(slot, strings.Join(keep, " "), newMachine)
}

// ApplyMachineCmdline swaps oldMachine for newMachine in every entry's
// options. vosd calls it after writing a new /var/lib/vos/cmdline.
func ApplyMachineCmdline(esp, oldMachine, newMachine string) error {
	return RewriteOptions(esp, func(e Entry) string {
		return SwapMachineArgs(e.Options, oldMachine, newMachine)
	})
}

// MachineCmdline reads /var/lib/vos/cmdline ("" if missing).
func MachineCmdline() string { return config.ReadLine(config.MachineCmdlinePath()) }

// SetMachineCmdline writes /var/lib/vos/cmdline under root ("" = live system).
func SetMachineCmdline(root, cmdline string) error {
	cmdline = strings.TrimSpace(cmdline)
	if strings.ContainsFunc(cmdline, isControl) {
		return errors.New("machine cmdline contains control characters")
	}
	path := config.MachineCmdlinePath()
	if root != "" {
		path = filepath.Join(root, path)
	}
	return config.WriteFileAtomic(path, []byte(cmdline+"\n"), 0o644)
}

// WriteLoaderConf writes loader/loader.conf (timeout 0, editor no, ...).
func WriteLoaderConf(esp string) error {
	dir := filepath.Join(esp, "loader")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "loader.conf"), []byte(LoaderConf))
}

// InstallEntry copies vmlinuz and initramfs.img from srcDir to
// esp/vos/<version>/ (tmp+rename) and writes vos-<version>[+tries].conf last.
// tries == 0 means no boot counting. options gets vos.slot=<slot> put
// first if it is not there already.
func InstallEntry(esp, version, slot, srcDir, options string, tries int) error {
	if !manifest.ValidVersion(version) {
		return fmt.Errorf("invalid version %q", version)
	}
	if slot != "a" && slot != "b" {
		return fmt.Errorf("invalid slot %q", slot)
	}
	if tries < 0 {
		return fmt.Errorf("invalid tries %d", tries)
	}
	if strings.ContainsFunc(options, isControl) {
		return errors.New("options contain control characters")
	}
	options = Cmdline(slot, options, "")

	name := entryPrefix + version
	if tries > 0 {
		name += "+" + strconv.Itoa(tries)
	}
	name += entrySuffix
	path := filepath.Join(entriesDir(esp), name)

	existing, err := Entries(esp)
	if err != nil {
		return err
	}
	// Both slots can hold the same version (a forced reinstall). Never let
	// one slot's entry silently replace the other's.
	for _, e := range existing {
		if e.Path == path && e.Slot != slot {
			return fmt.Errorf("%s already boots slot %s", name, e.Slot)
		}
	}

	kdir := filepath.Join(esp, "vos", version)
	if err := os.MkdirAll(kdir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(entriesDir(esp), 0o755); err != nil {
		return err
	}
	for _, f := range BootFiles {
		if err := copyAtomic(filepath.Join(srcDir, f), filepath.Join(kdir, f)); err != nil {
			return fmt.Errorf("installing %s: %w", f, err)
		}
	}
	// The entry goes last: until it exists, nothing boots the new files.
	if err := writeAtomic(path, []byte(entryText(version, options))); err != nil {
		return err
	}
	// The same slot and version under another counter would be a second
	// menu entry for the same thing.
	for _, e := range existing {
		if e.Slot == slot && e.Version == version && e.Path != path {
			if err := os.Remove(e.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	syncDir(entriesDir(esp))
	return nil
}

func entryText(version, options string) string {
	return "title VaporOS\n" +
		"version " + version + "\n" +
		"sort-key vapor\n" +
		"linux /vos/" + version + "/vmlinuz\n" +
		"initrd /vos/" + version + "/initramfs.img\n" +
		"options " + options + "\n"
}

// Entries lists vos-*.conf entries, sorted by file name. A missing entries
// directory is an empty list.
func Entries(esp string) ([]Entry, error) {
	dir := entriesDir(esp)
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || !strings.HasPrefix(name, entryPrefix) || !strings.HasSuffix(name, entrySuffix) {
			continue
		}
		e, err := readEntry(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue // renamed or removed while we listed
		}
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func readEntry(path string) (Entry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, err
	}
	base, counting, left, done := splitName(filepath.Base(path))
	e := Entry{Path: path, Counting: counting, Left: left, Done: done}
	var options []string
	for _, line := range strings.Split(string(b), "\n") {
		key, value := splitLine(line)
		switch key {
		case "version":
			e.Version = value
		case "options":
			// systemd-boot joins repeated options lines.
			options = append(options, value)
		case "linux":
			e.Linux = value
		case "initrd":
			e.Initrd = value
		}
	}
	e.Options = strings.Join(options, " ")
	if e.Version == "" {
		e.Version = strings.TrimPrefix(base, entryPrefix)
	}
	for _, a := range SplitArgs(e.Options) {
		if v, ok := strings.CutPrefix(a, "vos.slot="); ok {
			e.Slot = v
		}
	}
	return e, nil
}

// splitLine splits "key value..." as systemd-boot does: the key is the
// first word, the value the rest with surrounding space trimmed.
func splitLine(line string) (string, string) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return "", ""
	}
	i := strings.IndexAny(line, " \t")
	if i < 0 {
		return line, ""
	}
	return line[:i], strings.TrimSpace(line[i+1:])
}

// splitName splits "vos-<ver>[+LEFT[-DONE]].conf" into "vos-<ver>" and the
// counter. A '+' suffix that is not a valid counter is part of the name, as
// systemd-boot treats it.
func splitName(name string) (base string, counting bool, left, done int) {
	stem := strings.TrimSuffix(name, entrySuffix)
	i := strings.LastIndexByte(stem, '+')
	if i < 0 {
		return stem, false, 0, 0
	}
	l, d, hasDone := strings.Cut(stem[i+1:], "-")
	var err error
	if left, err = atoiDigits(l); err != nil {
		return stem, false, 0, 0
	}
	if hasDone {
		if done, err = atoiDigits(d); err != nil {
			return stem, false, 0, 0
		}
	}
	return stem[:i], true, left, done
}

func atoiDigits(s string) (int, error) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, errors.New("not a number")
	}
	return strconv.Atoi(s)
}

// EntryForSlot returns the entry booting slot, or nil. Should a slot have
// several entries, the one systemd-boot would pick wins: bootable first,
// then the newest version.
func EntryForSlot(esp, slot string) (*Entry, error) {
	es, err := Entries(esp)
	if err != nil {
		return nil, err
	}
	return SlotEntry(es, slot), nil
}

// SlotEntry is EntryForSlot over entries already listed.
func SlotEntry(es []Entry, slot string) *Entry {
	var best *Entry
	for i := range es {
		e := &es[i]
		if e.Slot != slot {
			continue
		}
		if best == nil || better(e, best) {
			best = e
		}
	}
	return best
}

// NextEntry returns the entry of es that a restart starts, nil when there
// is none. VaporOS entries share one sort-key, and stages and rollbacks
// clear systemd-boot's saved choices, so it starts the entry that sorts
// first across both slots: bootable first, then the newest version. On a
// tie the running slot's (booted) stays.
func NextEntry(es []Entry, booted string) *Entry {
	var next *Entry
	for i := range es {
		e := &es[i]
		if next == nil || better(e, next) || (e.Slot == booted && !better(next, e)) {
			next = e
		}
	}
	return next
}

func better(a, b *Entry) bool {
	if a.Bootable() != b.Bootable() {
		return a.Bootable()
	}
	return CompareVersions(a.Version, b.Version) > 0
}

// RemoveSlotEntries deletes entries for slot and their kernels once
// unreferenced.
func RemoveSlotEntries(esp, slot string) error {
	es, err := Entries(esp)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.Slot != slot {
			continue
		}
		if err := os.Remove(e.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	syncDir(entriesDir(esp))
	return PruneKernels(esp)
}

// PruneKernels removes every /vos/<ver>/ directory that no loader entry
// references, and temp files that an interrupted write left behind. Like the
// bash vos, it looks at every *.conf, not only ours.
func PruneKernels(esp string) error {
	confs, _ := filepath.Glob(filepath.Join(entriesDir(esp), "*.conf"))
	var refs strings.Builder
	for _, c := range confs {
		if b, err := os.ReadFile(c); err == nil {
			refs.Write(b)
			refs.WriteByte('\n')
		}
	}
	removeTemps(entriesDir(esp))
	vosDir := filepath.Join(esp, "vos")
	des, err := os.ReadDir(vosDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, d := range des {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(vosDir, d.Name())
		if strings.Contains(refs.String(), "/vos/"+d.Name()+"/") {
			removeTemps(dir)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	syncDir(vosDir)
	return nil
}

// removeTemps deletes ".<name>.tmp-*" files left by writeAtomic/copyAtomic.
func removeTemps(dir string) {
	tmps, _ := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	for _, t := range tmps {
		os.Remove(t)
	}
}

// RewriteOptions rewrites the options line of every entry via f(entry).
// An entry whose options f leaves unchanged is not touched.
func RewriteOptions(esp string, f func(e Entry) string) error {
	for range raceRetries {
		es, err := Entries(esp)
		if err != nil {
			return err
		}
		raced := false
		for _, e := range es {
			opts := f(e)
			if opts == e.Options {
				continue
			}
			if strings.ContainsFunc(opts, isControl) {
				return fmt.Errorf("%s: options contain control characters", e.Name())
			}
			ok, err := rewriteEntry(esp, e, opts)
			if err != nil {
				return err
			}
			raced = raced || !ok
		}
		if !raced {
			return nil
		}
	}
	return errors.New("boot entries keep changing under us; try again")
}

// rewriteEntry replaces e's options line and returns false if e was renamed
// meanwhile. It writes a temp file and renames it onto the entry's current
// name. systemd-bless-boot may rename the entry (dropping or changing its
// counter) at any moment. If it did so between our last look and our
// rename, our copy sits under the stale name next to the renamed original:
// it is removed again, and the caller lists afresh and retries.
func rewriteEntry(esp string, e Entry, opts string) (bool, error) {
	b, err := os.ReadFile(e.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(e.Path)
	tmp, err := writeTemp(dir, e.Name(), replaceOptions(string(b), opts))
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp) // a no-op once renamed
	if _, err := os.Stat(e.Path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	beforeRename()
	if err := os.Rename(tmp, e.Path); err != nil {
		return false, err
	}
	syncDir(dir)
	es, err := Entries(esp)
	if err != nil {
		return false, err
	}
	for _, o := range es {
		if o.Path != e.Path && o.Slot == e.Slot && o.Version == e.Version && o.Base() == e.Base() {
			os.Remove(e.Path)
			syncDir(dir)
			return false, nil
		}
	}
	return true, nil
}

// replaceOptions swaps the first options line for opts and drops any
// further ones (systemd-boot would append them).
func replaceOptions(text, opts string) []byte {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	out := make([]string, 0, len(lines)+1)
	done := false
	for _, l := range lines {
		if key, _ := splitLine(l); key == "options" {
			if !done {
				out = append(out, "options "+opts)
				done = true
			}
			continue
		}
		out = append(out, l)
	}
	if !done {
		out = append(out, "options "+opts)
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

// MarkBad renames the entry for slot to +0-1 so the other slot boots next.
// It renames whatever the entry is called at that moment: if
// systemd-bless-boot renamed it first, the rename fails with ENOENT, and
// MarkBad looks again. An entry already at +0 is left alone.
func MarkBad(esp, slot string) error {
	found := false
	err := renameEntries(esp, slot, func(e Entry) string {
		found = true
		if !e.Bootable() {
			return ""
		}
		return e.Base() + "+0-1" + entrySuffix
	})
	if err == nil && !found {
		return fmt.Errorf("slot %s has no boot entry", slot)
	}
	return err
}

// SetTries gives each of slot's entries that has run out of tries a fresh
// boot counter of tries (e.g. vos-<ver>+0-1.conf -> vos-<ver>+3.conf). A
// rollback uses it to re-arm a slot that an earlier rollback marked bad,
// while keeping the automatic fallback if that slot does not come up.
func SetTries(esp, slot string, tries int) error {
	if tries <= 0 {
		return fmt.Errorf("invalid tries %d", tries)
	}
	return renameEntries(esp, slot, func(e Entry) string {
		if e.Bootable() {
			return ""
		}
		return e.Base() + "+" + strconv.Itoa(tries) + entrySuffix
	})
}

// Bless drops the boot counter of slot's entries that ran out of tries
// (vos-<ver>+0-1.conf -> vos-<ver>.conf), as systemd-bless-boot does after a
// good boot. It is meant for the running slot, which demonstrably boots: an
// uncounted entry is a fallback systemd-boot never gives up on.
func Bless(esp, slot string) error {
	return renameEntries(esp, slot, func(e Entry) string {
		if e.Bootable() {
			return ""
		}
		return e.Base() + entrySuffix
	})
}

// renameEntries renames each of slot's entries to newName(e) ("" leaves it).
// It never renames onto an existing file: with the same version in both
// slots, the new name can be the other slot's entry, and replacing it would
// silently drop that slot from the menu. An entry systemd-bless-boot renamed
// under us (ENOENT) makes it list the entries again.
func renameEntries(esp, slot string, newName func(Entry) string) error {
	for range raceRetries {
		es, err := Entries(esp)
		if err != nil {
			return err
		}
		raced := false
		for _, e := range es {
			if e.Slot != slot {
				continue
			}
			name := newName(e)
			if name == "" || name == e.Name() {
				continue
			}
			err := renameNoReplace(e.Path, filepath.Join(filepath.Dir(e.Path), name))
			switch {
			case err == nil:
			case errors.Is(err, fs.ErrNotExist):
				raced = true
			case errors.Is(err, fs.ErrExist):
				return fmt.Errorf("cannot rename %s: %s already exists (another slot's entry)", e.Name(), name)
			default:
				return err
			}
		}
		if !raced {
			syncDir(entriesDir(esp))
			return nil
		}
	}
	return fmt.Errorf("boot entry for slot %s keeps changing under us; try again", slot)
}

// renameIfAbsent renames src to dst unless dst exists. It stands in where
// rename(2) cannot refuse by itself. The only other writer of the entries,
// systemd-bless-boot, renames just the booted entry and never onto another.
func renameIfAbsent(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: fs.ErrExist}
	}
	return os.Rename(src, dst)
}

// EnsureESP triggers the /efi automount and checks it is usable. /efi is a
// systemd automount, so the first lookup below it mounts the ESP, the same
// "ls /efi/loader" poke the bash vos did.
func EnsureESP(esp string) error {
	loader := filepath.Join(esp, "loader")
	fi, err := os.Stat(loader)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the ESP is not mounted at %s (no loader/ directory)", esp)
	}
	if err != nil {
		return fmt.Errorf("ESP at %s: %w", esp, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("ESP at %s: loader is not a directory", esp)
	}
	// The ESP is mode 0700: listing proves we can read it.
	if _, err := os.ReadDir(loader); err != nil {
		return fmt.Errorf("ESP at %s: %w", esp, err)
	}
	// fstab finds the ESP by partition label: with a second VaporOS disk
	// attached it could be that disk's, which the firmware never boots.
	return CheckESPDisk(esp)
}

// CompareVersions orders versions the way systemd-boot sorts entries of
// the same sort-key (a simplified strverscmp_improved): digit runs compare
// as numbers, letter runs as strings, separators only split. It returns
// -1, 0 or 1.
func CompareVersions(a, b string) int {
	for {
		a, b = strings.TrimLeftFunc(a, isVersionSep), strings.TrimLeftFunc(b, isVersionSep)
		switch {
		case a == "" && b == "":
			return 0
		case a == "":
			return -1
		case b == "":
			return 1
		}
		ra, restA := versionRun(a)
		rb, restB := versionRun(b)
		da, db := isDigit(ra[0]), isDigit(rb[0])
		switch {
		case da && db:
			ra, rb = strings.TrimLeft(ra, "0"), strings.TrimLeft(rb, "0")
			if len(ra) != len(rb) {
				return sign(len(ra) - len(rb))
			}
			if c := strings.Compare(ra, rb); c != 0 {
				return c
			}
		case da:
			return 1 // a number is newer than letters
		case db:
			return -1
		default:
			if c := strings.Compare(ra, rb); c != 0 {
				return c
			}
		}
		a, b = restA, restB
	}
}

// versionRun splits off the leading run of digits or of letters.
func versionRun(s string) (string, string) {
	digit := isDigit(s[0])
	i := 1
	for i < len(s) && !isVersionSep(rune(s[i])) && isDigit(s[i]) == digit {
		i++
	}
	return s[:i], s[i:]
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isVersionSep(r rune) bool {
	return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// writeAtomic writes data to path via a temp file in the same directory,
// fsync and rename. Unlike config.WriteFileAtomic it never chmods: that
// fails on FAT.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := writeTemp(dir, filepath.Base(path), data)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(dir)
	return nil
}

// writeTemp writes data to a fresh ".<name>.tmp-*" file in dir. The name
// matches neither vos-*.conf nor *.conf, so systemd-boot ignores it.
func writeTemp(dir, name string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// copyAtomic copies src to dst via a temp file next to dst.
func copyAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	dir := filepath.Dir(dst)
	f, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = io.Copy(f, in)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir makes renames in dir durable. Best effort: not every filesystem
// supports fsync on a directory.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
