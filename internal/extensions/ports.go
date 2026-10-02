package extensions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// sunshineAdminPort is never opened, whatever an extension asks: Sunshine's
// admin UI and API (docs/CONTRACTS.md "Firewall").
const sunshineAdminPort = 47990

// firewallUnit loads the ports file with the rest of the firewall.
const firewallUnit = "vos-firewall.service"

// exposed is a port an extension this boot runs opens to the local network.
type exposed struct {
	id       string
	name     string // the extension's name
	proto    string // "tcp" | "udp"
	port     int
	mode     string // "proxied": vosd serves it | "lan": its service does
	upstream string // proxied: 127.0.0.1:<port>
	services []descriptor.Service
}

// exposedPorts lists the network ports of the extensions this boot mounted
// that are still wanted (wanted ∪ core with their requirements), in the
// boot report's order: one removed until the restart closes its ports at
// once, and opens them again when it is added back.
func exposedPorts() ([]exposed, error) {
	rep, err := store.LoadBootReport()
	if err != nil {
		return nil, fmt.Errorf("boot report: %w", err)
	}
	if len(rep.Mounted) == 0 {
		return nil, nil
	}
	cat, err := catalog.Load(config.ExtCatalogPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	wanted, err := store.Wanted()
	if err != nil {
		return nil, err
	}
	want := wantSet(cat, wanted)
	var out []exposed
	for _, m := range rep.Mounted {
		if !want[m.ID] {
			continue
		}
		d, err := Shipped(m.ID)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				shippedErr(m.ID, err)
			}
			continue
		}
		shippedErr(m.ID, nil)
		if d.Network == nil {
			continue
		}
		for _, p := range d.Network.Ports {
			e := exposed{id: m.ID, name: d.Name, proto: p.Proto, port: p.Port, mode: p.Mode,
				upstream: p.Upstream, services: d.Services}
			if p.Port < 1024 || p.Port > 65535 || p.Port == sunshineAdminPort || e.upstreamPort() == sunshineAdminPort {
				continue
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// upstreamPort is the loopback port vosd proxies e to, 0 for none.
func (e exposed) upstreamPort() int {
	if e.mode != "proxied" {
		return 0
	}
	host, port, err := net.SplitHostPort(e.upstream)
	n, nerr := strconv.Atoi(port)
	if err != nil || nerr != nil || host != "127.0.0.1" || n < 1024 || n > 65535 {
		return 0
	}
	return n
}

// shippedErrs logs an error reading a shipped descriptor once per id,
// state of the file and error: vosd reads them every few seconds (the
// ports, the control center's document, steam.json).
var shippedErrs = &onceLog{last: map[string]string{}}

// shippedErr logs err, from reading id's shipped descriptor, unless it
// was logged for the file as it is (path, size and mtime); nil forgets
// id's.
func shippedErr(id string, err error) {
	state := ""
	if err != nil {
		path := filepath.Join(config.ExtDescriptorsDir, id+".json")
		state = path + " " + statKey(path)
	}
	shippedErrs.log(id, state, err)
}

type onceLog struct {
	mu   sync.Mutex
	last map[string]string
}

// log logs err for id unless it was the last one logged for id in the
// same state; nil forgets id's.
func (o *onceLog) log(id, state string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err == nil {
		delete(o.last, id)
		return
	}
	if key := state + "\x00" + err.Error(); o.last[id] != key {
		o.last[id] = key
		log.Printf("extensions: %s: %v", id, err)
	}
}

// portsFile is /var/lib/vos/ext/ports as vos-firewall reads it: one
// "<proto> <port>" line per port, "tcp <port> upstream <port>" for one
// vosd proxies to loopback (only root may connect to that), sorted, each
// once.
func portsFile(ps []exposed) []byte {
	var lines []string
	for _, p := range ps {
		l := fmt.Sprintf("%s %d", p.proto, p.port)
		if up := p.upstreamPort(); up != 0 && p.proto == "tcp" {
			l += fmt.Sprintf(" upstream %d", up)
		}
		lines = append(lines, l+"\n")
	}
	slices.Sort(lines)
	var b bytes.Buffer
	for _, l := range slices.Compact(lines) {
		b.WriteString(l)
	}
	return b.Bytes()
}

// firewallWait bounds the firewall's reload.
const firewallWait = 30 * time.Second

// syncPorts writes the ports file for the extensions this boot runs and,
// when it changed (or the last reload failed), reloads the firewall, which
// opens those ports to the local network only (docs/CONTRACTS.md
// "Firewall"). It also tells the web UIs' listeners to look again.
func (s *Service) syncPorts() {
	defer s.signal(s.web.kick)
	s.web.portsMu.Lock()
	defer s.web.portsMu.Unlock()
	ps, err := exposedPorts()
	if err != nil {
		log.Printf("extensions: ports: %v", err)
		return
	}
	b := portsFile(ps)
	cur, err := os.ReadFile(config.ExtPortsPath())
	unreadable := err != nil && !errors.Is(err, fs.ErrNotExist) // a missing file opens nothing, as an empty one
	if unreadable {
		log.Printf("extensions: ports: %v", err)
	}
	if unreadable || !bytes.Equal(cur, b) {
		if err := config.WriteFileAtomic(config.ExtPortsPath(), b, 0o644); err != nil {
			log.Printf("extensions: ports: %v", err)
			return
		}
		s.web.portsReload = true
	}
	if !s.web.portsReload {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), firewallWait)
	defer cancel()
	if err := s.cc.systemctl(ctx, false, "reload", firewallUnit); err != nil {
		log.Printf("extensions: reloading the firewall for the ports %q: %v", bytes.TrimSpace(b), err)
		return
	}
	s.web.portsReload = false
}
