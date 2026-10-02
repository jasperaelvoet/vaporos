package coolercontrol

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// cli runs `vos ext coolercontrol <step>`, the steps of
// coolercontrold.service (docs/CONTRACTS.md "Binary"): prepare and fans
// snapshot before each start, fans restore after each stop. Exit 1 stops a
// start; 2 is bad arguments.
func cli(args []string) int {
	log.SetFlags(0)
	log.SetPrefix("")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	var err error
	switch {
	case len(args) == 1 && args[0] == "prepare":
		err = prepare(ctx, unitDirs(), runBackup)
	case len(args) == 2 && args[0] == "fans" && args[1] == "snapshot":
		err = snapshotFans()
	case len(args) == 2 && args[0] == "fans" && args[1] == "restore":
		err = restoreFans()
	default:
		fmt.Fprintln(os.Stderr, "usage: vos ext coolercontrol prepare | fans snapshot | fans restore")
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vos ext coolercontrol:", err)
		return 1
	}
	return 0
}
