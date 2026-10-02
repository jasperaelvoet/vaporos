package extensions

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// webState is vosd's side of the extensions' network ports: the ports file
// the firewall reads (ports.go) and the web UIs vosd serves on their own
// ports while their extension runs (docs/CONTRACTS.md "HTTP API",
// Extension web UIs).
type webState struct {
	portsMu     sync.Mutex
	portsReload bool // the firewall has not loaded the ports file as it is

	kick    chan struct{} // the ports or the services may have changed
	lastErr string        // the last error listing the ports, logged once (runWeb's goroutine only)

	mu      sync.Mutex
	guard   func(w http.ResponseWriter, r *http.Request) bool // api.Server.GuardWeb
	failed  map[int]string                                    // by port: the last error starting its listener, logged once
	serving map[int]string                                    // by port: the extension whose web UI is served now (web_running)

	// Seams.
	state  func(ctx context.Context, unit string, user bool) string
	listen func(port int) (net.Listener, error)
}

func newWebState() webState {
	return webState{
		kick:    make(chan struct{}, 1),
		failed:  map[int]string{},
		serving: map[int]string{},
		state:   sysd.ActiveState,
		listen:  func(port int) (net.Listener, error) { return net.Listen("tcp", fmt.Sprintf(":%d", port)) },
	}
}

// webPoll is how often vosd looks whether an extension's services run, to
// serve its web UI or stop. A variable for tests.
var webPoll = 5 * time.Second

// webServer is one extension web UI being served.
type webServer struct {
	srv      *http.Server
	upstream string
	done     chan struct{}
}

func (w *webServer) close() {
	w.srv.Close()
	<-w.done
}

// SetWebGuard installs the access check of the extensions' web UIs
// (api.Server.GuardWeb). Without one every request is refused.
func (s *Service) SetWebGuard(guard func(w http.ResponseWriter, r *http.Request) bool) {
	s.web.mu.Lock()
	s.web.guard = guard
	s.web.mu.Unlock()
}

func (s *Service) webGuard() func(w http.ResponseWriter, r *http.Request) bool {
	s.web.mu.Lock()
	defer s.web.mu.Unlock()
	return s.web.guard
}

// runWeb serves the proxied ports of the extensions this boot runs, each
// only while its extension's services are active, until ctx ends. A port
// that cannot be bound is logged and tried again later; it never stops
// vosd.
func (s *Service) runWeb(ctx context.Context) {
	servers := map[int]*webServer{}
	defer func() {
		for port, w := range servers {
			w.close()
			s.setServing(port, "")
		}
	}()
	t := time.NewTicker(webPoll)
	defer t.Stop()
	for {
		s.serveWeb(ctx, servers)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.web.kick:
		}
	}
}

// serveWeb starts and stops the listeners in servers (by port) so that
// each proxied port of a running extension is served.
func (s *Service) serveWeb(ctx context.Context, servers map[int]*webServer) {
	ps, err := exposedPorts()
	if msg := fmt.Sprint(err); err != nil && msg != s.web.lastErr {
		log.Printf("extensions: web UIs: %v", err)
		s.web.lastErr = msg
	} else if err == nil {
		s.web.lastErr = ""
	}
	want := map[int]exposed{}
	for _, p := range ps {
		if p.mode == "proxied" && p.proto == "tcp" {
			want[p.port] = p
		}
	}
	for port, w := range servers {
		p, ok := want[port]
		if !ok || p.upstream != w.upstream || !s.webUp(ctx, p, true) {
			w.close()
			delete(servers, port)
			s.setServing(port, "")
			log.Printf("extensions: stopped serving :%d", port)
		}
	}
	for port, p := range want {
		if servers[port] != nil || !s.webUp(ctx, p, false) {
			continue
		}
		w, err := s.startWeb(ctx, p)
		s.web.mu.Lock()
		if err != nil {
			if s.web.failed[port] != err.Error() {
				log.Printf("extensions: %s: serving its web UI on :%d: %v (trying again)", p.id, port, err)
			}
			s.web.failed[port] = err.Error()
			s.web.mu.Unlock()
			continue
		}
		delete(s.web.failed, port)
		s.web.mu.Unlock()
		servers[port] = w
		s.setServing(port, p.id)
		log.Printf("extensions: %s: serving its web UI on :%d", p.id, port)
	}
}

// setServing records that port serves id's web UI now ("" for none) and
// tells the document, whose cards say so (web_running).
func (s *Service) setServing(port int, id string) {
	s.web.mu.Lock()
	if id == "" {
		delete(s.web.serving, port)
	} else {
		s.web.serving[port] = id
	}
	s.web.mu.Unlock()
	s.changed()
}

// serving is the extension whose web UI port serves now, or "".
func (s *Service) serving(port int) string {
	s.web.mu.Lock()
	defer s.web.mu.Unlock()
	return s.web.serving[port]
}

// webUp reports whether p's extension runs: each of its services (not
// templates, whose instances come and go) active. One that restarts
// (activating or reloading) keeps a port already served, so a short
// restart does not drop the page's connections.
func (s *Service) webUp(ctx context.Context, p exposed, serving bool) bool {
	for _, u := range p.services {
		if strings.Contains(u.Unit, "@.") {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		st := s.web.state(cctx, u.Unit, u.Scope == "user")
		cancel()
		switch {
		case st == "active":
		case serving && (st == "activating" || st == "reloading"):
		default:
			return false
		}
	}
	return true
}

func (s *Service) startWeb(ctx context.Context, p exposed) (*webServer, error) {
	ln, err := s.web.listen(p.port)
	if err != nil {
		return nil, err
	}
	w := &webServer{upstream: p.upstream, done: make(chan struct{})}
	w.srv = &http.Server{
		Handler:           s.proxyHandler(p),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          log.New(log.Writer(), "extensions: "+p.id+" web UI: ", 0),
	}
	go func() {
		defer close(w.done)
		if err := w.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("extensions: %s: web UI on :%d: %v", p.id, p.port, err)
		}
	}()
	return w, nil
}

// proxyHandler forwards what GuardWeb admits to the extension's own server
// on loopback. The page reaches it under the name and port the browser
// used; vosd's cookies and headers stay with vosd, and event streams flush
// as they come.
func (s *Service) proxyHandler(p exposed) http.Handler {
	target := &url.URL{Scheme: "http", Host: p.upstream}
	// Logged when it stops answering and when it answers again, not per
	// request: an open page asks every second.
	var down atomic.Bool
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
			stripVOS(pr.Out.Header)
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			if down.CompareAndSwap(true, false) {
				log.Printf("extensions: %s web UI: %s answers again", p.id, p.upstream)
			}
			dropVOSCookies(resp.Header)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if !errors.Is(err, context.Canceled) && down.CompareAndSwap(false, true) {
				log.Printf("extensions: %s web UI: %v", p.id, err)
			}
			http.Error(w, p.name+" isn't answering. Try again in a moment.", http.StatusBadGateway)
		},
		ErrorLog: log.New(log.Writer(), "extensions: "+p.id+" web UI: ", 0),
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		guard := s.webGuard()
		if guard == nil {
			http.Error(w, "VaporOS is still starting. Try again in a moment.", http.StatusServiceUnavailable)
			return
		}
		if guard(w, r) {
			rp.ServeHTTP(w, r)
		}
	})
}

// vosPrefix names vosd's cookies; vosHeader its headers (lower case).
const (
	vosPrefix = "vos_"
	vosHeader = "x-vos-"
)

// stripVOS removes vosd's headers and cookies from a request for an
// extension: its session token is vosd's alone.
func stripVOS(h http.Header) {
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), vosHeader) {
			delete(h, k)
		}
	}
	var kept []string
	for _, line := range h.Values("Cookie") {
		for _, c := range strings.Split(line, ";") {
			c = strings.TrimSpace(c)
			name, _, _ := strings.Cut(c, "=")
			if c != "" && !strings.HasPrefix(strings.TrimSpace(name), vosPrefix) {
				kept = append(kept, c)
			}
		}
	}
	h.Del("Cookie")
	if len(kept) > 0 {
		h.Set("Cookie", strings.Join(kept, "; "))
	}
}

// dropVOSCookies removes an extension's attempts to set vosd's cookies,
// which the browser would send to vosd too: cookies are per host, not per
// port.
func dropVOSCookies(h http.Header) {
	var kept []string
	for _, c := range h.Values("Set-Cookie") {
		name, _, _ := strings.Cut(c, "=")
		if !strings.HasPrefix(strings.TrimSpace(name), vosPrefix) {
			kept = append(kept, c)
		}
	}
	h.Del("Set-Cookie")
	for _, c := range kept {
		h.Add("Set-Cookie", c)
	}
}
