// Package boot manages the ESP: systemd-boot entries per slot with boot
// counting, kernels under /vos/<ver>/, loader.conf, and the kernel command
// line (vos.slot + image cmdline + machine cmdline). Ported from the bash vos.
package boot

import "errors"

var ErrNotImplemented = errors.New("not implemented")

type Entry struct {
	Path     string // full path of the .conf
	Version  string
	Slot     string
	Counting bool // has +N[-M]
	Left     int  // tries left (N)
	Done     int  // tries done (M)
	Options  string
}

// Cmdline composes an entry's options line.
func Cmdline(slot, imageCmdline, machineCmdline string) string { return "" }

// MachineCmdline reads /var/lib/vos/cmdline ("" if missing).
func MachineCmdline() string { return "" }

// SetMachineCmdline writes /var/lib/vos/cmdline under root ("" = live system).
func SetMachineCmdline(root, cmdline string) error { return ErrNotImplemented }

// WriteLoaderConf writes loader/loader.conf (timeout 0, editor no, ...).
func WriteLoaderConf(esp string) error { return ErrNotImplemented }

// InstallEntry copies vmlinuz and initramfs.img from srcDir to
// esp/vos/<version>/ (tmp+rename) and writes vos-<version>[+tries].conf last.
// tries == 0 means no boot counting.
func InstallEntry(esp, version, slot, srcDir, options string, tries int) error {
	return ErrNotImplemented
}

// Entries lists vos-*.conf entries.
func Entries(esp string) ([]Entry, error) { return nil, ErrNotImplemented }

// EntryForSlot returns the entry booting slot, or nil.
func EntryForSlot(esp, slot string) (*Entry, error) { return nil, ErrNotImplemented }

// RemoveSlotEntries deletes entries for slot and their kernels once unreferenced.
func RemoveSlotEntries(esp, slot string) error { return ErrNotImplemented }

// RewriteOptions rewrites the options line of every entry via f(entry).
func RewriteOptions(esp string, f func(e Entry) string) error { return ErrNotImplemented }

// MarkBad renames the entry for slot to +0-1 so the other slot boots next.
func MarkBad(esp, slot string) error { return ErrNotImplemented }

// EnsureESP triggers the /efi automount and checks it is usable.
func EnsureESP(esp string) error { return ErrNotImplemented }
