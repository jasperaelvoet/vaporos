package truckersmp

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// box is a gaming user's home, runtime directory and Steam libraries in
// temp dirs: the home's own library and one on a disk.
type box struct {
	t        *testing.T
	steam    string // ~/.local/share/Steam, a library
	disk     string // /var/mnt/<label>/SteamLibrary
	injector []byte // what the image ships
}

func newBox(t *testing.T) *box {
	t.Helper()
	dir := t.TempDir()
	vars := []*string{&config.GamerHome, &config.GamerRuntimeDir, &config.ExtMountedLibDir, &config.StateDir, &config.RunDir}
	saved := make([]string, len(vars))
	for i, v := range vars {
		saved[i] = *v
	}
	savedLibs := libraries
	t.Cleanup(func() {
		for i, v := range vars {
			*v = saved[i]
		}
		libraries = savedLibs
	})
	config.GamerHome = filepath.Join(dir, "home", "vapor")
	config.GamerRuntimeDir = filepath.Join(dir, "run", "user", "1000")
	config.ExtMountedLibDir = filepath.Join(dir, "usr", "lib", "vos", "ext")
	config.StateDir = filepath.Join(dir, "state")
	config.RunDir = filepath.Join(dir, "run", "vos")
	t.Setenv("XDG_RUNTIME_DIR", config.GamerRuntimeDir)
	b := &box{t: t,
		steam:    filepath.Join(config.GamerHome, ".local", "share", "Steam"),
		disk:     filepath.Join(dir, "mnt", "SATA1TB", "SteamLibrary"),
		injector: []byte("MZ\x90\x00 truckersmp-cli"),
	}
	for _, d := range []string{config.GamerRuntimeDir, b.steam, b.disk} {
		mkdir(t, d)
	}
	write(t, injectorSource(), string(b.injector))
	libraries = func() []string { return []string{b.steam, b.disk} }
	return b
}

// install puts g's appmanifest into lib.
func (b *box) install(lib string, g game) {
	write(b.t, filepath.Join(lib, "steamapps", "appmanifest_"+itoa(g.app)+".acf"),
		`"AppState" { "appid" "`+itoa(g.app)+`" "StateFlags" "4" }`)
}

// log writes g's game.log.txt in its Proton prefix in lib.
func (b *box) log(lib string, g game, version string) {
	write(b.t, filepath.Join(lib, prefixDocsRel(g), "game.log.txt"),
		"************ : log created on : Friday October 02 2026 @ 18:40:28\n"+
			"00:00:00.000 : "+g.docs+" init ver."+version+" (rev. 4a4ce8f7a3d9) win_x64 [Sep 15 2026 18:23:05]\n"+
			"00:00:00.010 : Steam API initialized\n")
}

func itoa(n uint32) string { return strconv.FormatUint(uint64(n), 10) }

func mkdir(t *testing.T, d string) {
	t.Helper()
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sum(b []byte) string {
	s := md5.Sum(b)
	return hex.EncodeToString(s[:])
}

// tmpServer is TruckersMP's three servers in one: the version API,
// files.json and the files, with Range support, counting what it serves.
type tmpServer struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	version  string
	files    map[string][]byte // FilePath without the leading slash
	types    map[string]string
	badCore  string            // an MD5 the API gives core_ets2mp.dll instead of its own
	noCore   bool              // the API gives core_ets2mp.dll no MD5
	noHead   bool              // HEAD is refused
	chunked  bool              // GETs say no length
	gets     map[string]int    // by path
	ranges   int               // GETs with a Range header
	cut      map[string]int    // path → bytes after which the next GET breaks off
	extraAPI map[string]string // more API fields
}

func newTMPServer(t *testing.T) *tmpServer {
	s := &tmpServer{t: t, version: "0.7.7.9", files: map[string][]byte{}, types: map[string]string{},
		gets: map[string]int{}, cut: map[string]int{}}
	s.add("core_ets2mp.dll", "ets2", bytes.Repeat([]byte("E"), 3000))
	s.add("core_atsmp.dll", "ats", bytes.Repeat([]byte("A"), 2000))
	s.add("data/ets2mp.adb", "ets2", bytes.Repeat([]byte("d"), 5000))
	s.add("ui/ui.zip", "system", bytes.Repeat([]byte("u"), 7000))
	s.add("launcher/readme.txt", "launcher", []byte("not ours"))
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	saved := [3]string{versionURL, filesURL, downloadBase}
	t.Cleanup(func() { versionURL, filesURL, downloadBase = saved[0], saved[1], saved[2] })
	versionURL, filesURL, downloadBase = s.srv.URL+"/v2/version", s.srv.URL+"/files.json", s.srv.URL+"/files"
	return s
}

func (s *tmpServer) add(path, typ string, content []byte) {
	s.files[path], s.types[path] = content, typ
}

func (s *tmpServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.URL.Path == "/v2/version":
		dll := sum(s.files["core_ets2mp.dll"])
		switch {
		case s.badCore != "":
			dll = s.badCore
		case s.noCore:
			dll = ""
		}
		v := map[string]any{"name": s.version, "numeric": "7790", "stage": "Release",
			"ets2mp_checksum":        map[string]string{"dll": strings.ToUpper(dll), "adb": "x"},
			"atsmp_checksum":         map[string]string{"dll": sum(s.files["core_atsmp.dll"]), "adb": "y"},
			"supported_game_version": "1.61.1.1s", "supported_ats_game_version": "1.61.3.1s"}
		json.NewEncoder(w).Encode(v)
	case r.URL.Path == "/files.json":
		var list []map[string]string
		var paths []string
		for p := range s.files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			list = append(list, map[string]string{"Md5": sum(s.files[p]), "Type": s.types[p], "FilePath": "/" + p})
		}
		json.NewEncoder(w).Encode(map[string]any{"Files": list})
	case strings.HasPrefix(r.URL.Path, "/files/"):
		p := strings.TrimPrefix(r.URL.Path, "/files/")
		content, ok := s.files[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodHead && s.noHead {
			http.Error(w, "no", http.StatusMethodNotAllowed)
			return
		}
		if r.Method == http.MethodGet {
			s.gets[p]++
			if r.Header.Get("Range") != "" {
				s.ranges++
			}
			if n, ok := s.cut[p]; ok {
				delete(s.cut, p)
				w.Header().Set("Content-Length", strconv.Itoa(len(content)))
				w.WriteHeader(http.StatusOK)
				w.Write(content[:n])
				return // the connection ends early
			}
			if s.chunked {
				w.WriteHeader(http.StatusOK)
				for i := 0; i < len(content); i += 1000 {
					w.Write(content[i:min(i+1000, len(content))])
					w.(http.Flusher).Flush()
				}
				return
			}
		}
		http.ServeContent(w, r, p, time.Time{}, bytes.NewReader(content))
	default:
		http.NotFound(w, r)
	}
}

func (s *tmpServer) getCount(p string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets[p]
}
