// Package daemon is vosd: it wires every service to the HTTP server and
// runs them until SIGTERM. In live mode it is the web installer.
package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

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

	version := system.Version()
	listening := make(chan net.Addr, 1)
	srv := api.New(api.Options{
		Addr:      fmt.Sprintf(":%d", config.HTTPPort),
		Installer: live,
		Version:   version,
		Ready:     func(a net.Addr) { listening <- a },
	})
	srv.SetSetupCode(setupCode(live))
	// web.allow_public is read from config.json as requests arrive, so a
	// hand edit applies without restarting vosd.
	srv.SetAllowPublic(newConfigWatch().allowPublic)
	web.Register(srv)

	disp := display.NewManager(cfg)
	disp.SetSetupCode(srv.SetupCode())
	disp.Routes(srv)
	sys := system.NewService(cfg)
	sys.Routes(srv)

	ann := newAnnouncer(live, version, srv.SetupCode)
	srv.OnSetupCodeChange(func(code string) {
		// First-run setup finished: the welcome screen, the serial line
		// and the next vosd start all stop offering the code.
		disp.SetSetupCode(code)
		if code == "" {
			forgetSetupCode()
		}
		ann.poke()
	})

	runners := []runner{{"display", disp.Run}}
	if live {
		install.NewService(cfg).Routes(srv)
	} else {
		up := update.NewService(cfg)
		sun := sunshine.NewService(cfg)
		sto := storage.NewService(cfg)
		pow := power.NewService(cfg, disp.Streaming, sun.Busy, up.Busy)
		// Someone using the web UI keeps the machine from idling off.
		srv.OnActivity(pow.Touch)
		for _, r := range []interface{ Routes(*api.Server) }{up, sun, sto, pow} {
			r.Routes(srv)
		}
		runners = append(runners,
			runner{"update", up.Run},
			runner{"sunshine", sun.Run},
			runner{"power", pow.Run},
			runner{"system", sys.Run})
		// The session socket (vos session begin|end) is served by disp.Run.
	}
	var wg sync.WaitGroup
	for _, r := range runners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			supervise(ctx, r)
		}()
	}
	go func() {
		select {
		case a := <-listening:
			mode := "os"
			if live {
				mode = "installer"
			}
			log.Printf("vosd %s (%s) listening on %s", version, mode, a)
		case <-ctx.Done():
			return
		}
		if err := sdNotify("READY=1"); err != nil {
			log.Printf("sd_notify: %v", err)
		}
		ann.run(ctx)
	}()

	err = srv.Run(ctx)
	_ = sdNotify("STOPPING=1")
	stop() // a listen error must stop the services too
	waitTimeout(&wg, stopTimeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vosd:", err)
		return 1
	}
	return 0
}

// stopTimeout bounds how long vosd waits for services to wind down (drop
// DRM master, close sockets) after SIGTERM; systemd allows 90 s.
const stopTimeout = 10 * time.Second

// firstRestartDelay is the wait before restarting a service that panicked;
// it doubles per panic up to a minute. A variable for tests.
var firstRestartDelay = time.Second

// runner is one long-lived service loop.
type runner struct {
	name string
	run  func(context.Context)
}

// supervise runs r until ctx ends. A panic is logged and the service is
// restarted with backoff instead of taking vosd down: the web UI must stay
// up, since it is how a broken image gets updated or rolled back. A Run
// that returns normally is done and is not restarted.
func supervise(ctx context.Context, r runner) {
	backoff := firstRestartDelay
	for {
		if !runRecovered(ctx, r) || ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, time.Minute)
	}
}

// runRecovered runs r once and reports whether it panicked.
func runRecovered(ctx context.Context, r runner) (panicked bool) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("%s: panic: %v\n%s", r.name, p, debug.Stack())
			panicked = true
		}
	}()
	r.run(ctx)
	return false
}

// waitTimeout waits for wg, giving up after d.
func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		log.Printf("vosd: services still stopping after %v; exiting anyway", d)
	}
}
