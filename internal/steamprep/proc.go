package steamprep

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// steamRunning reports whether a Steam client runs as uid: Steam rewrites
// its files from memory when it exits, so an edit under it would be lost.
// When procDir cannot be read it says yes.
func steamRunning(procDir string, uid int) bool {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if strings.TrimLeft(e.Name(), "0123456789") != "" {
			continue
		}
		dir := filepath.Join(procDir, e.Name())
		fi, err := os.Stat(dir)
		if err != nil {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(dir, "comm"))
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(comm)) {
		case "steam", "steam.sh", "steamwebhelper":
			return true
		}
	}
	return false
}
