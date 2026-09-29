package install

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/auth"
	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/display"
	"github.com/jasperaelvoet/vaporos/internal/storage"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// Runner runs an external command and returns its combined output. The
// installer shells out for everything that has a well-known tool (sgdisk,
// mkfs, mount, bootctl): every step is then easy to reproduce by hand, and
// tests swap in a fake that records the calls.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	return sysd.Run(ctx, name, args...)
}

// exitCode returns the exit status carried by a Runner error, or -1.
func exitCode(err error) int {
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return -1
}

// env is everything an install touches outside this package: the other
// VaporOS packages it calls and the command runner. Keeping them behind one
// struct lets tests drive a whole install against fakes, independent of how
// boot, auth or display are implemented. Fetching and verifying the image
// is deliberately not in here: tests sign real images with a test key, so
// the update package's checks run as they do on a machine.
type env struct {
	run Runner

	scanDisks         func(context.Context) ([]storage.Disk, error)
	gpu               func() display.GPUInfo
	chooseConnector   func() (string, error)
	machineCmdlineFor func(connector string) string

	bootCmdline       func(slot, imageCmdline, machineCmdline string) string
	writeLoaderConf   func(esp string) error
	installEntry      func(esp, version, slot, srcDir, options string, tries int) error
	setMachineCmdline func(root, cmdline string) error
	setAdminPassword  func(root, password string) error

	// sleep waits d or until ctx ends; tests make it instant.
	sleep func(ctx context.Context, d time.Duration) error
	logf  func(format string, args ...any)
}

func defaultEnv() *env {
	return &env{
		run:               execRunner{},
		scanDisks:         storage.ScanDisks,
		gpu:               display.Probe,
		chooseConnector:   display.ChooseVirtualConnector,
		machineCmdlineFor: display.MachineCmdlineFor,
		bootCmdline:       boot.Cmdline,
		writeLoaderConf:   boot.WriteLoaderConf,
		installEntry:      boot.InstallEntry,
		setMachineCmdline: boot.SetMachineCmdline,
		setAdminPassword:  auth.SetAdminPassword,
		sleep:             sleepCtx,
		logf:              log.Printf,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
