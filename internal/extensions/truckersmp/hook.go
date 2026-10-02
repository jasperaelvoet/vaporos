package truckersmp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
)

// defaultGameArgs is what the game gets when Steam passed it nothing: the
// OpenGL renderer, no intro videos, the 64-bit build.
var defaultGameArgs = []string{"-rdevice", "gl", "-nointro", "-64bit"}

// now is the clock; a variable for tests.
var now = time.Now

// LaunchHook runs as the gaming user inside `vos ext launch`, for ETS2,
// ATS and the extension's own shortcuts. A start of the game that
// `vos ext truckersmp mp` asked for (its flag) runs the injector instead
// of the game's executable: truckersmp-cli.exe GAMEDIR MODDIR [ARGS...]
// starts the game and loads the mod. Every other start passes untouched.
func (h *Helper) LaunchHook(ctx context.Context, l *extensions.Launch) error {
	if l.Shortcut != "" {
		shortcutArgs(l)
		return nil
	}
	g, ok := gameByApp(l.App)
	if !ok {
		return nil
	}
	i := exeIndex(l.Argv, g)
	if i < 0 {
		// The Linux build cannot load the mod. Its flag goes, so a later
		// single-player start stays single-player.
		if buildIndex(l.Argv, "linux_x64", strings.TrimSuffix(g.exe, ".exe")) >= 0 && takeFlag(flagPath(), g, now()) {
			return extensions.Refuse(msgLinux+g.key, fmt.Errorf("Steam started %s's Linux build for multiplayer", g.short))
		}
		return nil
	}
	if !takeFlag(flagPath(), g, now()) {
		return nil
	}
	home := homeDir()
	if err := quickCheck(home, g); err != nil {
		return extensions.Refuse(notReady(home, g), err)
	}
	injector := filepath.Join(home, binRel)
	if err := ensureInjector(injectorSource(), injector); err != nil {
		return extensions.Refuse(msgLauncher, fmt.Errorf("copying the injector: %w", err))
	}
	l.Argv = rewriteArgv(l.Argv, i, injector, filepath.Join(home, filesRel))
	return nil
}

// exeIndex is where g's Windows executable (GAMEDIR/bin/win_x64/<exe>) is
// in a launch's command, -1 when it is not there: Steam's command for
// the Linux build, an installer step or anything else is not rewritten.
func exeIndex(argv []string, g game) int { return buildIndex(argv, "win_x64", g.exe) }

// buildIndex is where an absolute GAMEDIR/bin/<build>/<exe> is in argv
// (any case), -1 when it is not there.
func buildIndex(argv []string, build, exe string) int {
	for i, a := range argv {
		if !filepath.IsAbs(a) {
			continue
		}
		dir, name := filepath.Split(filepath.Clean(a))
		dir = filepath.Clean(dir)
		if strings.EqualFold(name, exe) && strings.EqualFold(filepath.Base(dir), build) &&
			strings.EqualFold(filepath.Base(filepath.Dir(dir)), "bin") {
			return i
		}
	}
	return -1
}

// rewriteArgv puts the injector where the game's executable is, followed
// by GAMEDIR (two levels above the executable's folder) and MODDIR, then
// the game's own arguments, or defaultGameArgs when Steam gave none.
func rewriteArgv(argv []string, i int, injector, modDir string) []string {
	gameDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Clean(argv[i]))))
	out := slices.Clone(argv[:i])
	out = append(out, injector, gameDir, modDir)
	if rest := argv[i+1:]; len(rest) > 0 {
		return append(out, rest...)
	}
	return append(out, defaultGameArgs...)
}

// mpArgs are the words after vos that start multiplayer for g.
func mpArgs(g game) []string { return []string{"ext", ID, "mp", g.key} }

// shortcutArgs completes a shortcut's command: the shortcut's executable
// is vos, and `vos steam prepare` puts its arguments in its launch options
// after %command%. Where they are missing (a prepare that does not write
// them yet), the hook adds them, so the shortcut still starts multiplayer.
func shortcutArgs(l *extensions.Launch) {
	key, ok := strings.CutPrefix(l.Shortcut, ID+"/")
	g, known := gameByKey(key)
	if !ok || !known || len(l.Argv) == 0 || l.Argv[len(l.Argv)-1] != vosBin {
		return
	}
	l.Argv = append(l.Argv, mpArgs(g)...)
}

// maxInjector bounds the injector's size (it is a small program).
const maxInjector = 64 << 20

// ensureInjector makes dst a copy of src, the injector the image ships:
// Proton's container sees the home but not /usr/lib/vos. A copy that
// already matches is left alone.
func ensureInjector(src, dst string) error {
	want, err := readBounded(src, maxInjector)
	if err != nil {
		return err
	}
	if have, err := readBounded(dst, maxInjector); err == nil && bytes.Equal(have, want) {
		return nil
	}
	return config.WriteFileAtomic(dst, want, 0o644)
}

func readBounded(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New(path + " is too large")
	}
	return b, nil
}
