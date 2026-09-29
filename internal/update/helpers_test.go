package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

const (
	bootedVersion  = "20260901.000000"
	bootedRollback = 100
	oldIdleVersion = "20260801.000000"
	slotSize       = 1 << 20
	imageCmdline   = "quiet loglevel=3 console=ttyS0,115200"
	machineArgs    = "video=DP-1:e drm.edid_firmware=DP-1:edid/vaporos.bin"
)

// testEnv is a fake installed machine in a temp dir: config paths, both
// slot "partitions" (1 MiB files), an ESP with slot a (booted) and an older
// slot b, a trusted key, and the machine cmdline.
type testEnv struct {
	t    *testing.T
	dir  string
	priv ed25519.PrivateKey
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	strs := []*string{&config.StateDir, &config.RunDir, &config.ESP, &config.KeysDir, &config.ImageInfoPath,
		&config.ImageCmdlinePath, &config.ProcCmdline, &config.OSReleasePath, &config.LiveMedium,
		&WorkDir, &MountInfoPath, &BootCountVar, &boot.PartLabelDir, &boot.SysClassBlock, &boot.SysDevBlock,
		&boot.DevDir, &boot.MountInfoPath, &boot.EFIVarsDir}
	saved := make([]string, len(strs))
	for i, p := range strs {
		saved[i] = *p
	}
	oldBackoff, oldStall, oldSync, oldClient := backoff, stallTimeout, syncEvery, httpClient
	oldPoll, oldPing := healthPoll, pingTimeout
	t.Cleanup(func() {
		for i, p := range strs {
			*p = saved[i]
		}
		backoff, stallTimeout, syncEvery, httpClient = oldBackoff, oldStall, oldSync, oldClient
		healthPoll, pingTimeout = oldPoll, oldPing
	})
	backoff = func(int) time.Duration { return time.Millisecond }
	syncEvery = 64 << 10

	config.StateDir = filepath.Join(dir, "state")
	config.RunDir = filepath.Join(dir, "run")
	config.ESP = filepath.Join(dir, "esp")
	config.KeysDir = filepath.Join(dir, "keys")
	config.ImageInfoPath = filepath.Join(dir, "image.json")
	config.ImageCmdlinePath = filepath.Join(dir, "image-cmdline")
	config.ProcCmdline = filepath.Join(dir, "proc-cmdline")
	config.OSReleasePath = filepath.Join(dir, "os-release")
	config.LiveMedium = filepath.Join(dir, "medium")
	WorkDir = filepath.Join(dir, "tmp")
	MountInfoPath = filepath.Join(dir, "mountinfo")
	BootCountVar = filepath.Join(dir, "LoaderBootCountPath")
	// No sysfs and no mountinfo for the boot disk: slots go by label.
	boot.PartLabelDir = filepath.Join(dir, "dev", "disk", "by-partlabel")
	boot.SysClassBlock = filepath.Join(dir, "sys", "class", "block")
	boot.SysDevBlock = filepath.Join(dir, "sys", "dev", "block")
	boot.DevDir = filepath.Join(dir, "dev")
	boot.MountInfoPath = filepath.Join(dir, "boot-mountinfo")
	boot.EFIVarsDir = filepath.Join(dir, "efivars")

	e := &testEnv{t: t, dir: dir}
	for _, d := range []string{config.StateDir, config.KeysDir, boot.PartLabelDir, boot.EFIVarsDir, filepath.Join(config.ESP, "loader", "entries")} {
		e.must(os.MkdirAll(d, 0o755))
	}
	e.write(config.ImageInfoPath, fmt.Sprintf(`{"version":%q,"channel":"main","rollback_index":%d}`, bootedVersion, bootedRollback))
	e.write(config.ImageCmdlinePath, imageCmdline+"\n")
	e.bootSlot("a")
	for _, s := range []string{"a", "b"} {
		e.must(os.WriteFile(e.slotDev(s), make([]byte, slotSize), 0o644))
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	e.priv = priv
	e.write(filepath.Join(config.KeysDir, "test.pub"), manifest.EncodePublicKey(pub))
	e.must(boot.SetMachineCmdline("", machineArgs))

	kdir := t.TempDir()
	e.write(filepath.Join(kdir, "vmlinuz"), "old kernel")
	e.write(filepath.Join(kdir, "initramfs.img"), "old initrd")
	e.must(boot.InstallEntry(config.ESP, bootedVersion, "a", kdir, boot.Cmdline("a", imageCmdline, machineArgs), 0))
	e.must(boot.InstallEntry(config.ESP, oldIdleVersion, "b", kdir, boot.Cmdline("b", imageCmdline, machineArgs), 0))
	return e
}

func (e *testEnv) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) write(path, content string) {
	e.t.Helper()
	e.must(os.MkdirAll(filepath.Dir(path), 0o755))
	e.must(os.WriteFile(path, []byte(content), 0o644))
}

// slotDev is where Stage writes slot.
func (e *testEnv) slotDev(slot string) string {
	e.t.Helper()
	dev, err := SlotDevice(slot)
	e.must(err)
	return dev
}

func (e *testEnv) bootSlot(slot string) {
	e.write(config.ProcCmdline, "vos.slot="+slot+" "+imageCmdline+" "+machineArgs+"\n")
}

func (e *testEnv) setState(st *State) {
	e.t.Helper()
	e.must(config.WriteJSONAtomic(config.UpdateStatePath(), st, 0o644))
}

func (e *testEnv) state() *State {
	e.t.Helper()
	st, err := LoadState()
	e.must(err)
	return st
}

func (e *testEnv) entry(slot string) *boot.Entry {
	e.t.Helper()
	en, err := boot.EntryForSlot(config.ESP, slot)
	e.must(err)
	return en
}

func (e *testEnv) cfg(source string) *config.Config {
	c := config.Defaults()
	c.Update.Source = source
	return c
}

// image is a built update: the five files, keyed by name.
type image struct {
	m     *manifest.Manifest
	files map[string][]byte
}

func (i *image) root() []byte { return i.files["root.erofs"] }

// makeImage builds and signs an image. mutate may change the manifest
// before it is signed; the files stay as built.
func (e *testEnv) makeImage(version string, rollback int64, rootSize int, mutate func(*manifest.Manifest)) *image {
	e.t.Helper()
	rng := mrand.New(mrand.NewPCG(uint64(rollback), uint64(rootSize)))
	root := make([]byte, rootSize)
	for i := range root {
		root[i] = byte(rng.Uint32())
	}
	files := map[string][]byte{
		"root.erofs":    root,
		"vmlinuz":       []byte("kernel " + version),
		"initramfs.img": []byte("initrd " + version),
	}
	art := func(name string) manifest.Artifact {
		sum := sha256.Sum256(files[name])
		return manifest.Artifact{Name: name, Size: int64(len(files[name])), SHA256: hex.EncodeToString(sum[:])}
	}
	m := &manifest.Manifest{
		Schema: 1, Product: "vaporos", Version: version, RollbackIndex: rollback, Channel: "main",
		Git: "abc", Created: "2026-09-29T12:34:56Z", Kernel: "7.2.8-1-cachyos",
		Cmdline: "quiet loglevel=3 panic=10", MinUpdater: 1,
		Artifacts: map[string]manifest.Artifact{
			"root": art("root.erofs"), "kernel": art("vmlinuz"), "initrd": art("initramfs.img"),
		},
	}
	if mutate != nil {
		mutate(m)
	}
	raw, err := json.Marshal(m)
	e.must(err)
	files["manifest.json"] = raw
	files["manifest.json.sig"] = manifest.Sign(raw, e.priv)
	return &image{m: m, files: files}
}

// srcDir writes the image to a directory source.
func (e *testEnv) srcDir(img *image) string {
	e.t.Helper()
	d, err := os.MkdirTemp(e.dir, "src-")
	e.must(err)
	for name, b := range img.files {
		e.must(os.WriteFile(filepath.Join(d, name), b, 0o644))
	}
	return d
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// fakeRegistry is an OCI registry that behaves like ghcr: an anonymous
// bearer token from a challenge, manifests by tag or digest, and blobs that
// 307 to a separate storage server with a fresh signed URL each time.
type fakeRegistry struct {
	t          *testing.T
	reg, store *httptest.Server
	repo, tag  string
	serveIndex bool

	blobs           map[string][]byte // digest -> content
	manifest, index []byte
	rootDigest      string
	failRoot        atomic.Bool // drop the next root download half way
	// holdRoot, when set, stops each root download half way until the
	// client gives up, and receives once the half is out.
	holdRoot chan struct{}

	tokenReqs, rootBlobReqs atomic.Int32
	mu                      sync.Mutex
	rootRanges, rootSigs    []string
	sig                     int
}

func newFakeRegistry(t *testing.T, img *image) *fakeRegistry {
	t.Helper()
	f := &fakeRegistry{t: t, repo: "jasperaelvoet/vaporos", tag: "main", blobs: map[string][]byte{}}
	var layers []map[string]any
	for _, name := range []string{"manifest.json", "manifest.json.sig", "root.erofs", "vmlinuz", "initramfs.img"} {
		b := img.files[name]
		d := sha(b)
		f.blobs[d] = b
		layers = append(layers, map[string]any{
			"mediaType": "application/octet-stream", "digest": d, "size": len(b),
			"annotations": map[string]string{titleAnnotation: name},
		})
	}
	f.rootDigest = sha(img.root())
	f.blobs[sha([]byte("{}"))] = []byte("{}")
	f.manifest, _ = json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": mediaOCIManifest, "artifactType": "application/vnd.vaporos.image.v1",
		"config": map[string]any{"mediaType": "application/vnd.oci.empty.v1+json", "digest": sha([]byte("{}")), "size": 2},
		"layers": layers,
	})
	f.index, _ = json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": mediaOCIIndex,
		"manifests": []map[string]any{{"mediaType": mediaOCIManifest, "digest": sha(f.manifest), "size": len(f.manifest)}},
	})
	f.store = httptest.NewTLSServer(http.HandlerFunc(f.storage))
	f.reg = httptest.NewTLSServer(http.HandlerFunc(f.registry))
	t.Cleanup(func() { f.reg.Close(); f.store.Close() })
	// httptest TLS servers share one certificate, so this client trusts both.
	httpClient = f.reg.Client()
	return f
}

func (f *fakeRegistry) spec() string {
	return "oci://" + strings.TrimPrefix(f.reg.URL, "https://") + "/" + f.repo
}

func (f *fakeRegistry) registry(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		f.tokenReqs.Add(1)
		q := r.URL.Query()
		if q.Get("scope") != "repository:"+f.repo+":pull" || q.Get("service") != "fake-registry" {
			http.Error(w, "bad token request "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"token":"tok"}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake-registry",scope="repository:%s:pull"`, f.reg.URL, f.repo))
		http.Error(w, `{"errors":[{"code":"UNAUTHORIZED"}]}`, http.StatusUnauthorized)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/v2/"+f.repo+"/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if ref, ok := strings.CutPrefix(rest, "manifests/"); ok {
		if !strings.Contains(r.Header.Get("Accept"), mediaOCIManifest) {
			http.Error(w, "no OCI Accept", http.StatusNotAcceptable)
			return
		}
		body, mt := f.manifest, mediaOCIManifest
		switch {
		case ref == f.tag && f.serveIndex, ref == sha(f.index):
			body, mt = f.index, mediaOCIIndex
		case ref == f.tag, ref == sha(f.manifest):
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mt)
		w.Header().Set("Docker-Content-Digest", sha(body))
		w.Write(body)
		return
	}
	if d, ok := strings.CutPrefix(rest, "blobs/"); ok {
		if _, ok := f.blobs[d]; !ok {
			http.NotFound(w, r)
			return
		}
		if d == f.rootDigest {
			f.rootBlobReqs.Add(1)
		}
		f.mu.Lock()
		f.sig++
		n := f.sig
		f.mu.Unlock()
		http.Redirect(w, r, fmt.Sprintf("%s/storage/%s?sig=%d", f.store.URL, d, n), http.StatusTemporaryRedirect)
		return
	}
	http.NotFound(w, r)
}

func (f *fakeRegistry) storage(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "" {
		http.Error(w, "the registry token must not reach storage", http.StatusBadRequest)
		return
	}
	d := strings.TrimPrefix(r.URL.Path, "/storage/")
	data, ok := f.blobs[d]
	if !ok || r.URL.Query().Get("sig") == "" {
		http.NotFound(w, r)
		return
	}
	if d == f.rootDigest {
		f.mu.Lock()
		f.rootRanges = append(f.rootRanges, r.Header.Get("Range"))
		f.rootSigs = append(f.rootSigs, r.URL.Query().Get("sig"))
		f.mu.Unlock()
		if f.holdRoot != nil {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			w.Write(data[:len(data)/2])
			w.(http.Flusher).Flush()
			select {
			case f.holdRoot <- struct{}{}:
			case <-r.Context().Done():
			}
			<-r.Context().Done()
			panic(http.ErrAbortHandler)
		}
		if f.failRoot.CompareAndSwap(true, false) {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			w.Write(data[:len(data)/2])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // the connection drops half way
		}
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}
