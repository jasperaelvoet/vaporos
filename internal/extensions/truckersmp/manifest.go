package truckersmp

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// manifest is what the last sync wrote (manifest.json in the home data
// area): the mod's version, when TruckersMP was last asked, the games
// whose files it holds and every file with its size and mtime, so a
// launch can tell quickly whether the files are still the ones the sync
// checked. Only a finished sync writes it, and a sync that changes files
// deletes it just before it moves its checked downloads into place: a
// manifest on disk always describes complete files.
type manifest struct {
	Version string         `json:"version"`
	Checked time.Time      `json:"checked"`
	Games   []string       `json:"games"`
	Files   []manifestFile `json:"files"`
}

type manifestFile struct {
	Path  string `json:"path"` // relative to MODDIR
	Type  string `json:"type"` // "system", "ets2" or "ats"
	MD5   string `json:"md5"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // unix nanoseconds
}

const maxManifest = 1 << 20

func parseManifest(b []byte) (*manifest, error) {
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	return &m, nil
}

// readManifest reads the manifest as its owner; nil without one.
func readManifest(home string) (*manifest, error) {
	f, err := os.Open(filepath.Join(home, manifestRel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxManifest+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxManifest {
		return nil, errors.New("manifest.json is too large")
	}
	return parseManifest(b)
}

func (m *manifest) has(g game) bool { return m != nil && slices.Contains(m.Games, g.key) }

func (m *manifest) entry(path string) (manifestFile, bool) {
	if m != nil {
		for _, f := range m.Files {
			if f.Path == path {
				return f, true
			}
		}
	}
	return manifestFile{}, false
}

// errStale is a quick check that found the files not ready.
var errStale = errors.New("its files are updating. Try again in a few minutes.")

// quickCheck tells whether g's files are complete, as fast as a launch
// allows: every file g needs is where the manifest says, with its size
// and mtime, and g's core library also has its MD5.
func quickCheck(home string, g game) error {
	m, err := readManifest(home)
	if err != nil || !m.has(g) {
		return errStale
	}
	core := false
	for _, f := range m.Files {
		if f.Type != "system" && f.Type != g.key {
			continue
		}
		p := filepath.Join(home, filesRel, filepath.FromSlash(f.Path))
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() != f.Size || fi.ModTime().UnixNano() != f.MTime {
			return errStale
		}
		if f.Path == g.coreDLL {
			if sum, err := fileMD5(p); err != nil || sum != f.MD5 {
				return errStale
			}
			core = true
		}
	}
	if !core {
		return errStale
	}
	return nil
}

// fileMD5 is a file's MD5, which is how TruckersMP names its files'
// content.
func fileMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
