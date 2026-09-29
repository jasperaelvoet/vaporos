package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

// flakyServer serves dir, but the first GET of each name in cut returns
// only half the body and then drops the connection.
type flakyServer struct {
	dir string
	cut map[string]bool

	mu       sync.Mutex
	requests []string
}

func (s *flakyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/images/")
	s.mu.Lock()
	s.requests = append(s.requests, name+" "+r.Header.Get("Range"))
	cut := s.cut[name]
	s.cut[name] = false
	s.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if cut {
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(http.StatusOK)
		w.Write(b[:len(b)/2])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
}

func TestInstallFromHTTP(t *testing.T) {
	f, _ := installMachine(t)
	srcDir := t.TempDir()
	img := writeImage(t, srcDir, 3<<20+7)
	fs := &flakyServer{dir: srcDir, cut: map[string]bool{"root.erofs": true, "vmlinuz": true}}
	srv := httptest.NewServer(fs)
	defer srv.Close()

	r := f.runner()
	var recs []progressRec
	if err := runInstall(context.Background(), f.env(r), Options{Disk: "sda", Source: srv.URL + "/images"}, recorder(&recs)); err != nil {
		t.Fatal(err)
	}
	if !slotHas(t, f.path("dev/sda2"), img.root) {
		t.Error("slot a differs from the served image")
	}
	// Kernel and initrd were staged in a temp dir that is gone again.
	if f.entry.srcDir == "" || exists(f.entry.srcDir) {
		t.Errorf("entry source %q", f.entry.srcDir)
	}
	// Both cut downloads resumed where they stopped.
	fs.mu.Lock()
	reqs := strings.Join(fs.requests, "|")
	fs.mu.Unlock()
	half := strconv.Itoa(len(img.root) / 2)
	if !strings.Contains(reqs, "root.erofs bytes="+half+"-") {
		t.Errorf("root.erofs did not resume at %s: %s", half, reqs)
	}
	if !strings.Contains(reqs, "vmlinuz bytes=") {
		t.Errorf("vmlinuz did not resume: %s", reqs)
	}
}

// fakeRegistry is a small anonymous OCI registry that behaves like ghcr:
// a bearer token from the challenge, manifests by tag, and blobs that
// redirect to a storage URL which must not see the token. Plain HTTP, so
// sources are oci+http://.
type fakeRegistry struct {
	t     *testing.T
	srv   *httptest.Server
	repo  string
	tags  map[string][]byte // tag -> OCI manifest
	blobs map[string][]byte // digest -> content

	mu        sync.Mutex
	tagsAsked []string
}

// newFakeRegistry publishes the image in dir under tags. mutate may change
// the layer descriptors before the OCI manifest is built.
func newFakeRegistry(t *testing.T, dir string, tags []string, mutate func(layers []map[string]any)) *fakeRegistry {
	t.Helper()
	g := &fakeRegistry{t: t, repo: "jasperaelvoet/vaporos", tags: map[string][]byte{}, blobs: map[string][]byte{}}
	var layers []map[string]any
	for _, name := range []string{"manifest.json", "manifest.json.sig", "root.erofs", "vmlinuz", "initramfs.img"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		d := "sha256:" + sha(b)
		g.blobs[d] = b
		layers = append(layers, map[string]any{
			"mediaType": "application/octet-stream", "digest": d, "size": len(b),
			"annotations": map[string]string{"org.opencontainers.image.title": name},
		})
	}
	if mutate != nil {
		mutate(layers)
	}
	m, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "layers": layers,
	})
	for _, tag := range tags {
		g.tags[tag] = m
	}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeRegistry) spec() string {
	return "oci+http://" + strings.TrimPrefix(g.srv.URL, "http://") + "/" + g.repo
}

func (g *fakeRegistry) serve(w http.ResponseWriter, r *http.Request) {
	if d, ok := strings.CutPrefix(r.URL.Path, "/storage/"); ok {
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "the registry token must not reach storage", http.StatusBadRequest)
			return
		}
		b, ok := g.blobs[d]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
		return
	}
	if r.URL.Path == "/token" {
		fmt.Fprint(w, `{"token":"tok"}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="repository:%s:pull"`, g.srv.URL, g.repo))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/v2/"+g.repo+"/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if tag, ok := strings.CutPrefix(rest, "manifests/"); ok {
		g.mu.Lock()
		g.tagsAsked = append(g.tagsAsked, tag)
		g.mu.Unlock()
		m, ok := g.tags[tag]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Docker-Content-Digest", "sha256:"+sha(m))
		w.Write(m)
		return
	}
	if d, ok := strings.CutPrefix(rest, "blobs/"); ok {
		http.Redirect(w, r, g.srv.URL+"/storage/"+d, http.StatusTemporaryRedirect)
		return
	}
	http.NotFound(w, r)
}

func (g *fakeRegistry) asked() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return strings.Join(g.tagsAsked, ",")
}

func TestInstallFromOCI(t *testing.T) {
	for _, tc := range []struct {
		name, channel, imageChannel, wantTag string
	}{
		{"channel of the live image", "", "testing", "testing"},
		{"no image.json", "", "", "main"},
		{"explicit channel", "beta", "testing", "beta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := installMachine(t)
			if tc.imageChannel != "" {
				f.setImageInfo("20260101.000000", tc.imageChannel, false)
			}
			srcDir := t.TempDir()
			img := writeImage(t, srcDir, 2<<20+99)
			reg := newFakeRegistry(t, srcDir, []string{tc.wantTag}, nil)
			r := f.runner()
			var recs []progressRec
			opts := Options{Disk: "sda", Source: reg.spec(), Channel: tc.channel}
			if err := runInstall(context.Background(), f.env(r), opts, recorder(&recs)); err != nil {
				t.Fatal(err)
			}
			if got := reg.asked(); got != tc.wantTag {
				t.Errorf("registry was asked for %q, want %q", got, tc.wantTag)
			}
			if !slotHas(t, f.path("dev/sda2"), img.root) {
				t.Error("slot a differs from the pulled image")
			}
			if f.entry.version != img.man.Version || exists(f.entry.srcDir) {
				t.Errorf("entry = %+v", f.entry)
			}
			if !strings.Contains(recs[1].message, reg.spec()+":"+tc.wantTag) {
				t.Errorf("progress does not name the source: %q", recs[1].message)
			}
		})
	}
}

func TestInstallFromOCIRefusesBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(layers []map[string]any)
		tags   []string
		want   string
	}{
		{"layer is not the signed file", func(layers []map[string]any) {
			layers[2]["size"] = layers[2]["size"].(int) + 1
		}, []string{"main"}, "is not the file the manifest signs"},
		{"no such channel", nil, []string{"other"}, "no such tag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := installMachine(t)
			srcDir := t.TempDir()
			writeImage(t, srcDir, 1<<20)
			reg := newFakeRegistry(t, srcDir, tc.tags, tc.mutate)
			r := f.runner()
			err := runInstall(context.Background(), f.env(r), Options{Disk: "sda", Source: reg.spec()}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if d := destructive(r); len(d) > 0 {
				t.Fatalf("ran %v before refusing", d)
			}
		})
	}
}

// A kernel that does not match its manifest fails the install while the
// disk is still untouched: boot files are fetched before partitioning.
func TestInstallBootFilesBeforeErasing(t *testing.T) {
	f, img := installMachine(t)
	k := filepath.Join(img.dir, "vmlinuz")
	b, _ := os.ReadFile(k)
	b[0] ^= 0xff
	os.WriteFile(k, b, 0o644)
	r := f.runner()
	err := runInstall(context.Background(), f.env(r), Options{Disk: "sda"}, nil)
	if !errors.Is(err, update.ErrChecksum) || !strings.HasPrefix(err.Error(), StepProbe+": ") {
		t.Fatalf("err = %v", err)
	}
	if d := destructive(r); len(d) > 0 {
		t.Fatalf("ran %v after a bad kernel", d)
	}
}

func TestUnsignedRule(t *testing.T) {
	live := &imageSource{spec: sourceSpec{kind: srcLive}}
	dirSrc, err := openSource(sourceSpec{kind: srcDir, dir: t.TempDir()}, "")
	if err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{Version: "20260929.123456"}
	debug := &config.ImageInfo{Version: "20260929.123456", Debug: true}
	release := &config.ImageInfo{Version: "20260929.123456"}
	other := &config.ImageInfo{Version: "20260101.000000", Debug: true}
	for _, tc := range []struct {
		name    string
		src     *imageSource
		running *config.ImageInfo
		m       *manifest.Manifest
		want    string // "" = allowed
	}{
		{"debug live medium", live, debug, m, ""},
		{"debug live medium, before reading the manifest", live, debug, nil, ""},
		{"release live medium", live, release, m, "only debug builds"},
		{"no image.json", live, nil, m, "only debug builds"},
		{"not the running image", live, other, m, "not the running debug image"},
		{"a directory, even on a debug build", dirSrc, debug, m, "only the live medium"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := unsignedRule(tc.src, tc.running, tc.m)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, errUnsigned) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestInstallUnsigned(t *testing.T) {
	unsign := func(f *fakeSys, img *image) { os.Remove(filepath.Join(img.dir, "manifest.json.sig")) }
	for _, tc := range []struct {
		name  string
		setup func(f *fakeSys, img *image) Options
		want  string // "" = installs
	}{
		{"release medium", func(f *fakeSys, img *image) Options {
			unsign(f, img)
			f.setImageInfo(img.man.Version, "main", false)
			return Options{Disk: "sda"}
		}, "only debug builds may install without a signature"},
		{"medium without image.json", func(f *fakeSys, img *image) Options {
			unsign(f, img)
			return Options{Disk: "sda"}
		}, "only debug builds"},
		{"debug medium", func(f *fakeSys, img *image) Options {
			unsign(f, img)
			f.setImageInfo(img.man.Version, "main", true)
			return Options{Disk: "sda"}
		}, ""},
		{"debug build, someone else's manifest", func(f *fakeSys, img *image) Options {
			unsign(f, img)
			f.setImageInfo("20260101.000000", "main", true)
			return Options{Disk: "sda"}
		}, "not the running debug image"},
		{"debug build, unsigned directory", func(f *fakeSys, img *image) Options {
			f.setImageInfo(img.man.Version, "main", true)
			dir := t.TempDir()
			writeImage(t, dir, 1<<20)
			os.Remove(filepath.Join(dir, "manifest.json.sig"))
			return Options{Disk: "sda", Source: dir}
		}, "only the live medium"},
		{"debug build, forged signature", func(f *fakeSys, img *image) Options {
			os.WriteFile(filepath.Join(img.dir, "manifest.json.sig"), manifest.Sign([]byte("other"), testKey), 0o644)
			f.setImageInfo(img.man.Version, "main", true)
			return Options{Disk: "sda"}
		}, "not signed by a trusted key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, img := installMachine(t)
			opts := tc.setup(f, img)
			r := f.runner()
			err := runInstall(context.Background(), f.env(r), opts, nil)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if !slotHas(t, f.path("dev/sda2"), img.root) {
					t.Error("slot a differs from the image")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if d := destructive(r); len(d) > 0 {
				t.Fatalf("ran %v before refusing", d)
			}
		})
	}
}

// An unsigned debug medium is still checked file by file: a damaged root
// fails the install, and the summary says the image is unsigned.
func TestInstallUnsignedStillVerifiesFiles(t *testing.T) {
	f, img := installMachine(t)
	os.Remove(filepath.Join(img.dir, "manifest.json.sig"))
	f.setImageInfo(img.man.Version, "main", true)

	in, err := newInstaller(f.env(f.runner()), Options{Disk: "sda"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !in.unsigned || !strings.Contains(strings.Join(in.summary(), "\n"), "UNSIGNED debug build") {
		t.Errorf("unsigned = %v, summary:\n%s", in.unsigned, strings.Join(in.summary(), "\n"))
	}
	in.close()

	root := filepath.Join(img.dir, "root.erofs")
	b, _ := os.ReadFile(root)
	b[len(b)/2] ^= 1
	os.WriteFile(root, b, 0o644)
	r := f.runner()
	err = runInstall(context.Background(), f.env(r), Options{Disk: "sda"}, nil)
	if !errors.Is(err, update.ErrChecksum) || !strings.HasPrefix(err.Error(), StepWrite+": ") || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("err = %v", err)
	}
	if exists(filepath.Join(config.RunDir, "target/esp/loader/entries/vos-"+img.man.Version+".conf")) {
		t.Error("boot entry written for a damaged image")
	}
}

func TestParseSource(t *testing.T) {
	for _, tc := range []struct{ in, kind, dir string }{
		{"", srcLive, ""},
		{"live", srcLive, ""},
		{"/srv/vos/out", srcDir, "/srv/vos/out"},
		{"file:///srv/vos/out/", srcDir, "/srv/vos/out"},
		{"https://example.com/vos/", srcHTTP, ""},
		{"oci://ghcr.io/jasperaelvoet/vaporos", srcOCI, ""},
		{"oci://ghcr.io/jasperaelvoet/vaporos:dev-tooling", srcOCI, ""},
		{"oci+http://builder:5000/vaporos", srcOCI, ""},
	} {
		s, err := parseSource(tc.in)
		if err != nil || s.kind != tc.kind || s.dir != tc.dir {
			t.Errorf("parseSource(%q) = %+v, %v", tc.in, s, err)
		}
	}
	for _, bad := range []string{"http://", "file://relative/x", "ftp://x/", "out/", "oci://ghcr.io", "oci://ghcr.io/Bad Repo", "oci://ghcr.io/x/y:bad tag"} {
		if _, err := parseSource(bad); err == nil {
			t.Errorf("parseSource(%q) accepted", bad)
		}
	}
}
