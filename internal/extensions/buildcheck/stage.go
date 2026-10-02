package buildcheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/extensions/fsverity"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// What `vos ext catalog` writes into its output directory.
const (
	CatalogFile     = "extensions.list" // ships as /usr/lib/vos/extensions.list
	ManifestFile    = "extensions.json" // the manifest's "extensions" object
	DescriptorsDir  = "descriptors"     // <id>.json, ships as /usr/share/vos/extensions/
	erofsMagic      = 0xE0F5E1E2
	erofsMagicAt    = 1024
	maxPackagesFile = 1 << 20
)

// maxLowerdir bounds the overlay's lowerdir option for a set of every
// image: the initramfs mounts /usr with /run/vos/x/<n>/usr per image plus
// the root's, and mount options must fit in a page.
const maxLowerdir = 3800

// Staged is what `vos ext catalog` makes of a stage directory, which holds
// per extension id: ext-<id>.raw (the image), <id>.json (its source
// descriptor), <id>.build.json (check-tree's --json output) and optionally
// <id>.key (the build's input key) and <id>.packages.txt (the packages the
// image holds, "name version" per line).
type Staged struct {
	Catalog     *catalog.Catalog
	Extensions  map[string]manifest.Extension
	Descriptors map[string]*descriptor.Descriptor // with the build section
}

// ReadStage reads, hashes and cross-checks a stage directory.
func ReadStage(dir string) (*Staged, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, e := range ents {
		names[e.Name()] = true
	}
	var errs []error
	var ids []string
	for _, e := range ents {
		n := e.Name()
		if id, ok := cutAffixes(n, "ext-", ".raw"); ok {
			if !manifest.ValidExtensionID(id) {
				errs = append(errs, fmt.Errorf("%s: invalid extension id %q", n, id))
				continue
			}
			ids = append(ids, id)
		} else if id, ok := stagedJSONID(n); ok {
			if !names[manifest.ExtensionFile(id)] {
				errs = append(errs, fmt.Errorf("%s: no %s next to it", n, manifest.ExtensionFile(id)))
			}
		}
	}
	slices.Sort(ids)

	s := &Staged{Extensions: map[string]manifest.Extension{}, Descriptors: map[string]*descriptor.Descriptor{}}
	for _, id := range ids {
		x, d, err := readStaged(dir, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			continue
		}
		s.Extensions[id] = x
		s.Descriptors[id] = d
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := manifest.ValidateExtensions(s.Extensions); err != nil {
		return nil, err
	}
	if err := steamRules(s.Descriptors); err != nil {
		return nil, err
	}
	s.Catalog = catalog.FromManifest(&manifest.Manifest{Extensions: s.Extensions})
	if _, err := catalog.Parse(bytes.NewReader(s.Catalog.Format())); err != nil {
		return nil, fmt.Errorf("the catalog does not read back: %w", err)
	}
	if n := lowerdirLen(s.Catalog.IDs()); n > maxLowerdir {
		return nil, fmt.Errorf("mounting every extension would need a %d-byte lowerdir, more than %d", n, maxLowerdir)
	}
	return s, nil
}

func readStaged(dir, id string) (manifest.Extension, *descriptor.Descriptor, error) {
	var x manifest.Extension
	d, err := descriptor.Load(filepath.Join(dir, id+".json"))
	if err != nil {
		return x, nil, err
	}
	if err := d.ValidateSource(); err != nil {
		return x, nil, err
	}
	if d.ID != id {
		return x, nil, fmt.Errorf("%s.json is the descriptor of %q", id, d.ID)
	}
	res, err := readResult(filepath.Join(dir, id+".build.json"))
	if err != nil {
		return x, nil, err
	}
	perms, err := checkedPermissions(d, res.Permissions)
	if err != nil {
		return x, nil, err
	}
	size, sum, fsv, err := hashImage(filepath.Join(dir, manifest.ExtensionFile(id)))
	if err != nil {
		return x, nil, err
	}
	key, err := os.ReadFile(filepath.Join(dir, id+".key"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return x, nil, err
	}
	pkgs, err := readPackages(filepath.Join(dir, id+".packages.txt"))
	if err != nil {
		return x, nil, err
	}
	reqs := slices.Clone(d.Requires)
	slices.Sort(reqs)
	x = manifest.Extension{
		Name:     manifest.ExtensionFile(id),
		Size:     size,
		SHA256:   sum,
		FSVerity: fsv,
		Core:     d.Core,
		Requires: slices.Compact(reqs),
		Key:      strings.TrimSpace(string(key)),
	}
	d.Build = &descriptor.Build{Size: size, Packages: pkgs, Permissions: perms, RunsAsRoot: res.RunsAsRoot}
	return x, d, nil
}

// readResult reads check-tree's --json output; unknown fields are an error,
// so a stale or foreign file is never taken for one.
func readResult(file string) (*Result, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var r Result
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(file), err)
	}
	return &r, nil
}

// checkedPermissions returns the verified permissions in
// descriptor.Permissions order, failing unless they are the declared ones.
func checkedPermissions(d *descriptor.Descriptor, verified []string) ([]string, error) {
	out := []string{}
	for _, p := range descriptor.Permissions {
		if slices.Contains(verified, p) {
			out = append(out, p)
		}
	}
	for _, p := range verified {
		if !slices.Contains(descriptor.Permissions, p) {
			return nil, fmt.Errorf("build.json lists an unknown permission %q", p)
		}
	}
	declared := slices.Clone(d.Permissions)
	got := slices.Clone(out)
	slices.Sort(declared)
	slices.Sort(got)
	if !slices.Equal(declared, got) {
		return nil, fmt.Errorf("the build verified permissions %q, the descriptor declares %q", out, d.Permissions)
	}
	return out, nil
}

// hashImage returns an image's size, sha256 and fs-verity digest in one
// read, after checking that it is an erofs.
func hashImage(file string) (int64, string, string, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, "", "", err
	}
	defer f.Close()
	var magic [4]byte
	if _, err := f.ReadAt(magic[:], erofsMagicAt); err != nil || binary.LittleEndian.Uint32(magic[:]) != erofsMagic {
		return 0, "", "", fmt.Errorf("%s: not an erofs image", filepath.Base(file))
	}
	h := sha256.New()
	fsv, size, err := fsverity.Digest(io.TeeReader(f, h))
	if err != nil {
		return 0, "", "", err
	}
	return size, hex.EncodeToString(h.Sum(nil)), fsv, nil
}

func readPackages(file string) ([]string, error) {
	b, err := readLimited(file, maxPackagesFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, strings.Join(strings.Fields(line), " "))
		}
	}
	return out, nil
}

// steamRules are the catalog-wide Steam rules: one default compatibility
// tool, and one extension at most forcing a tool on any app.
func steamRules(ds map[string]*descriptor.Descriptor) error {
	var errs []error
	def := ""
	forced := map[uint32]string{}
	for _, id := range sortedKeys(ds) {
		st := ds[id].Steam
		if st == nil {
			continue
		}
		if st.DefaultCompatTool != "" {
			if def != "" {
				errs = append(errs, fmt.Errorf("%s and %s both set Steam's default compatibility tool", def, id))
			}
			def = id
		}
		for _, app := range st.ForceCompatTool {
			if other, ok := forced[app]; ok && other != id {
				errs = append(errs, fmt.Errorf("%s and %s both force a compatibility tool on app %d", other, id, app))
			}
			forced[app] = id
		}
	}
	return errors.Join(errs...)
}

// lowerdirLen is the longest lowerdir the initramfs can build from these
// ids (mount points named by position or by id, whichever is longer).
func lowerdirLen(ids []string) int {
	n := len("/new_root/usr")
	for i, id := range ids {
		n += len("/run/vos/x/") + max(len(id), len(strconv.Itoa(i+1))) + len("/usr:")
	}
	return n
}

// Write puts the catalog, the manifest entries and the descriptors into
// out, and removes descriptors of extensions no longer staged.
func (s *Staged) Write(out string) error {
	ddir := filepath.Join(out, DescriptorsDir)
	if err := os.MkdirAll(ddir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, CatalogFile), s.Catalog.Format(), 0o644); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, ManifestFile), s.Extensions); err != nil {
		return err
	}
	for id, d := range s.Descriptors {
		if err := writeJSON(filepath.Join(ddir, id+".json"), d); err != nil {
			return err
		}
	}
	ents, err := os.ReadDir(ddir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if _, staged := s.Descriptors[id]; ok && !staged {
			if err := os.Remove(filepath.Join(ddir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeJSON(file string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(b, '\n'), 0o644)
}

// stagedJSONID returns the id of <id>.json or <id>.build.json.
func stagedJSONID(name string) (string, bool) {
	if id, ok := strings.CutSuffix(name, ".build.json"); ok {
		return id, true
	}
	return strings.CutSuffix(name, ".json")
}

func cutAffixes(s, prefix, suffix string) (string, bool) {
	s, ok1 := strings.CutPrefix(s, prefix)
	s, ok2 := strings.CutSuffix(s, suffix)
	return s, ok1 && ok2 && s != ""
}
