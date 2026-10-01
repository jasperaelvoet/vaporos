package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// lsblkColumns are the columns ScanDisks needs. MOUNTPOINTS (plural, every
// mount of a filesystem) needs util-linux 2.37; older versions only know
// MOUNTPOINT, which lsblkJSON falls back to.
const lsblkColumns = "NAME,PATH,PKNAME,TYPE,SIZE,MODEL,TRAN,RM,UUID,LABEL,FSTYPE,MOUNTPOINTS,PARTLABEL,FSAVAIL"

// runLsblk runs lsblk; a variable so tests can feed fixtures.
var runLsblk = func(ctx context.Context, columns string) ([]byte, error) {
	out, err := sysd.Run(ctx, "lsblk", "-J", "-b", "-o", columns)
	return []byte(out), err
}

func lsblkJSON(ctx context.Context) ([]byte, error) {
	out, err := runLsblk(ctx, lsblkColumns)
	if err != nil {
		out, err = runLsblk(ctx, strings.Replace(lsblkColumns, "MOUNTPOINTS", "MOUNTPOINT", 1))
	}
	return out, err
}

// lsblkDev is one node of `lsblk -J`. Field types are deliberately loose:
// depending on the util-linux version, sizes are numbers or strings and
// booleans are true/false or "0"/"1", and any string may be null.
type lsblkDev struct {
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	PKName      string     `json:"pkname"`
	Type        string     `json:"type"`
	Size        flexInt    `json:"size"`
	Model       string     `json:"model"`
	Tran        string     `json:"tran"`
	RM          flexBool   `json:"rm"`
	UUID        string     `json:"uuid"`
	Label       string     `json:"label"`
	FSType      string     `json:"fstype"`
	Mountpoints []*string  `json:"mountpoints"`
	Mountpoint  *string    `json:"mountpoint"`
	PartLabel   string     `json:"partlabel"`
	FSAvail     flexInt    `json:"fsavail"`
	Children    []lsblkDev `json:"children"`
}

type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// null, or a human-readable size from an lsblk that ignored -b:
		// unknown is better than failing the whole scan.
		n = 0
	}
	*f = flexInt(n)
	return nil
}

type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	switch strings.Trim(strings.TrimSpace(string(b)), `"`) {
	case "true", "1":
		*f = true
	default:
		*f = false
	}
	return nil
}

// systemMounts are mountpoints that only a VaporOS system (or live) disk
// can have; they back up the partition-label check.
var systemMounts = map[string]bool{
	"/": true, "/state": true, "/efi": true, "/usr": true, "/var": true, "/run/vos/medium": true,
	// The partition the live ISO is an .iso file on (a Ventoy stick).
	"/run/vos/host": true,
}

// parseLsblk flattens lsblk's tree into Disk entries: every disk, then its
// partitions and anything stacked on them (crypt, LVM, RAID), each once.
// Partitions inherit their disk's model, transport and removable flag.
//
// Everything on a system disk is marked IsSystem, so it is never adopted
// or probed. On an installed system that is the disk holding vos_data (the
// running one, or an older install on a second disk). On the live ISO it
// is only the live medium: a vos_data disk there is an existing install
// the installer may repair or replace, so it must stay visible.
func parseLsblk(data []byte, liveLabel string, live bool) ([]Disk, error) {
	var tree struct {
		Blockdevices []lsblkDev `json:"blockdevices"`
	}
	if err := json.Unmarshal(data, &tree); err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	seen := map[string]bool{}
	var out []Disk
	for i := range tree.Blockdevices {
		top := &tree.Blockdevices[i]
		if skipTopLevel(top) {
			continue
		}
		system := isSystemTree(top, liveLabel, live)
		var walk func(d *lsblkDev, parent string)
		walk = func(d *lsblkDev, parent string) {
			path := d.Path
			if path == "" {
				path = "/dev/" + d.Name
			}
			if !seen[path] {
				seen[path] = true
				out = append(out, Disk{
					Path:      path,
					Parent:    parent,
					Type:      d.Type,
					Model:     firstNonEmpty(strings.TrimSpace(d.Model), strings.TrimSpace(top.Model)),
					Size:      int64(d.Size),
					Transport: firstNonEmpty(d.Tran, top.Tran),
					Removable: bool(d.RM) || bool(top.RM),
					UUID:      d.UUID,
					Label:     d.Label,
					FSType:    d.FSType,
					PartLabel: d.PartLabel,
					MountedAt: pickMount(d),
					IsSystem:  system,
					Free:      int64(d.FSAvail),
				})
			}
			for j := range d.Children {
				walk(&d.Children[j], path)
			}
		}
		walk(top, "")
	}
	return out, nil
}

// skipTopLevel drops block devices that are not disks a user could mean:
// loop devices (the live root image, snaps), zram swap and ramdisks.
func skipTopLevel(d *lsblkDev) bool {
	if d.Type == "loop" {
		return true
	}
	return strings.HasPrefix(d.Name, "zram") || strings.HasPrefix(d.Name, "ram")
}

func isSystemTree(d *lsblkDev, liveLabel string, live bool) bool {
	if (!live && d.PartLabel == "vos_data") || (liveLabel != "" && d.Label == liveLabel) {
		return true
	}
	for _, m := range mounts(d) {
		if systemMounts[m] {
			return true
		}
	}
	for i := range d.Children {
		if isSystemTree(&d.Children[i], liveLabel, live) {
			return true
		}
	}
	return false
}

// mounts returns the real mountpoints of d (never "[SWAP]").
func mounts(d *lsblkDev) []string {
	var out []string
	add := func(p *string) {
		if p != nil && strings.HasPrefix(*p, "/") {
			out = append(out, *p)
		}
	}
	for _, p := range d.Mountpoints {
		add(p)
	}
	if len(out) == 0 {
		add(d.Mountpoint)
	}
	return out
}

// pickMount chooses the mountpoint to show for a filesystem mounted more
// than once: a game library mount first, else the first one lsblk lists.
func pickMount(d *lsblkDev) string {
	ms := mounts(d)
	for _, m := range ms {
		if strings.HasPrefix(m, "/var/mnt/") || strings.HasPrefix(m, "/mnt/") {
			return m
		}
	}
	if len(ms) > 0 {
		return ms[0]
	}
	return ""
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// liveLabel is the filesystem label of the live medium (vos.label=, as the
// initramfs hook reads it).
func liveLabel() string {
	if v, ok := config.KernelArg("vos.label"); ok && v != "" {
		return v
	}
	return "VOS_LIVE"
}
