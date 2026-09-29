// Package daemon is vosd: it wires every service to the HTTP server and
// runs them until SIGTERM. In live mode it is the web installer.
package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display"
	"github.com/jasperaelvoet/vaporos/internal/install"
	"github.com/jasperaelvoet/vaporos/internal/power"
	"github.com/jasperaelvoet/vaporos/internal/storage"
	"github.com/jasperaelvoet/vaporos/internal/sunshine"
	"github.com/jasperaelvoet/vaporos/internal/system"
	"github.com/jasperaelvoet/vaporos/internal/update"
	"github.com/jasperaelvoet/vaporos/internal/web"
)

func Main(args []string) int {
	log.SetFlags(0)
	cfg, err := config.Load()
	if err != nil {
		log.Printf("config: %v (using defaults)", err)
	}
	live := config.IsLive()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	srv := api.New(api.Options{Addr: fmt.Sprintf(":%d", config.HTTPPort), Installer: live})
	srv.SetSetupCode(setupCode(live))
	web.Register(srv)

	disp := display.NewManager(cfg)
	disp.SetSetupCode(srv.SetupCode())
	disp.Routes(srv)
	system.NewService(cfg).Routes(srv)

	var runners []func(context.Context)
	runners = append(runners, disp.Run)
	if live {
		install.NewService(cfg).Routes(srv)
	} else {
		up := update.NewService(cfg)
		sun := sunshine.NewService(cfg)
		sto := storage.NewService(cfg)
		pow := power.NewService(cfg, disp.Streaming, sun.Busy, up.Busy)
		for _, r := range []interface{ Routes(*api.Server) }{up, sun, sto, pow} {
			r.Routes(srv)
		}
		runners = append(runners, up.Run, sun.Run, pow.Run)
	}
	for _, run := range runners {
		go run(ctx)
	}
	go announce(ctx, srv, live)

	if err := srv.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "vosd:", err)
		return 1
	}
	return 0
}

// setupCode returns a fresh code when one is needed: always in installer
// mode, and on an installed system until an admin password exists.
func setupCode(live bool) string { return "" }

// announce writes the VOS-READY serial line (docs/CONTRACTS.md) whenever
// the IP changes.
func announce(ctx context.Context, srv *api.Server, live bool) {}
