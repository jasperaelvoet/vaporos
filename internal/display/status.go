package display

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/brand"
	"github.com/jasperaelvoet/vaporos/internal/display/welcome"
	"github.com/jasperaelvoet/vaporos/internal/events"
)

// How long transient notices stay on the welcome screen without a refresh.
const (
	progressTTL = 2 * time.Minute // update/pairing/power progress without news
	messageTTL  = 5 * time.Minute
	// sleepNotice is how long before an idle shutdown the screen says so.
	sleepNotice = 2 * time.Minute
	// bootGrace keeps "no network" calm while links come up after a boot.
	bootGrace = 15 * time.Second
)

// statusOverlay is what other services told us through the event hub.
type statusOverlay struct {
	install   *installProgress
	update    *updateProgress
	updateAt  time.Time
	staged    string
	pairing   *pairingPending
	pairingAt time.Time
	message   string
	messageAt time.Time
	// sleepIn is power.idle's shutdown_in (seconds) as of sleepAt; a zero
	// sleepAt means no shutdown is counting down.
	sleepIn int
	sleepAt time.Time
}

// Event payloads (docs/CONTRACTS.md "Events").
type installProgress struct {
	Step    string `json:"step"`
	Percent int    `json:"percent"`
	Message string `json:"message"`
	State   string `json:"state"`
}

type updateProgress struct {
	Phase   string `json:"phase"`
	Percent int    `json:"percent"`
	Bytes   int64  `json:"bytes"`
	Total   int64  `json:"total"`
	Version string `json:"version"`
}

type updateState struct {
	Staged *struct {
		Version string `json:"version"`
	} `json:"staged"`
}

type pairingPending struct {
	Name string `json:"name"`
	// Pending is optional; a publisher sends false to clear the notice.
	Pending *bool `json:"pending"`
}

type powerIdle struct {
	ShutdownIn *int `json:"shutdown_in"`
}

type systemMessage struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// apply folds one event into the overlay. It reports whether anything the
// welcome screen shows may have changed.
func (o *statusOverlay) apply(ev events.Event, now time.Time) bool {
	switch ev.Topic {
	case "install.progress":
		var p installProgress
		if json.Unmarshal(ev.Data, &p) == nil {
			o.install = &p
		}
	case "update.progress":
		var p updateProgress
		if json.Unmarshal(ev.Data, &p) == nil {
			o.update, o.updateAt = &p, now
		}
	case "update.state":
		var s updateState
		if json.Unmarshal(ev.Data, &s) == nil {
			o.staged = ""
			if s.Staged != nil {
				o.staged = s.Staged.Version
			}
		}
	case "pairing.pending":
		var p pairingPending
		if json.Unmarshal(ev.Data, &p) != nil {
			p = pairingPending{}
		}
		if p.Pending != nil && !*p.Pending {
			o.pairing = nil
		} else {
			o.pairing, o.pairingAt = &p, now
		}
	case "system.message":
		var m systemMessage
		if json.Unmarshal(ev.Data, &m) == nil {
			o.message, o.messageAt = m.Text, now
		}
	case "power.idle":
		var p powerIdle
		if json.Unmarshal(ev.Data, &p) == nil {
			o.sleepAt = time.Time{}
			if p.ShutdownIn != nil {
				o.sleepIn, o.sleepAt = *p.ShutdownIn, now
			}
		}
	default:
		return false
	}
	return true
}

// updateActive reports whether an update download or write is under way.
func (o *statusOverlay) updateActive(now time.Time) bool {
	if o.update == nil || now.Sub(o.updateAt) > progressTTL {
		return false
	}
	switch strings.ToLower(o.update.Phase) {
	case "", "idle", "done", "staged", "failed", "error", "complete", "cancelled":
		return false
	}
	return true
}

// sleepSoon reports the time left before an idle shutdown that is at most
// sleepNotice away.
func (o *statusOverlay) sleepSoon(now time.Time) (time.Duration, bool) {
	if o.sleepAt.IsZero() || now.Sub(o.sleepAt) > progressTTL {
		return 0, false
	}
	left := time.Duration(o.sleepIn)*time.Second - now.Sub(o.sleepAt)
	if left > sleepNotice {
		return 0, false
	}
	return max(left, 0), true
}

// welcomeInputs is everything the welcome screen depends on.
type welcomeInputs struct {
	live         bool
	hostname     string
	ips          []string
	code         string
	version      string
	https        bool
	port         int
	gpuSupported bool
	gpuName      string
	// virtualPending: the virtual display is configured but the running
	// kernel does not force it on yet (a restart applies it).
	virtualPending bool
	session        *sessionInfo
	overlay        statusOverlay
	now            time.Time
	upSince        time.Time // when vosd started (zero: long ago)
}

// hostURL formats scheme://host[:port], bracketing IPv6 literals.
func hostURL(https bool, host string, port int) string {
	scheme, def := "http", 80
	if https {
		scheme, def = "https", 443
	}
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		host = "[" + host + "]"
	}
	if port != 0 && port != def && !https {
		host += ":" + strconv.Itoa(port)
	}
	return scheme + "://" + host
}

// buildWelcome decides what the welcome screen says.
func buildWelcome(in welcomeInputs) welcome.State {
	host := in.hostname
	if host == "" {
		host = "vapor"
	}
	st := welcome.State{
		Mode:     "os",
		Hostname: host,
		URL:      hostURL(in.https, host+".local", in.port),
		Code:     in.code,
		Title:    "VaporOS",
		Version:  in.version,
	}
	if in.live {
		st.Mode = "installer"
	}
	base := st.URL
	if len(in.ips) > 0 {
		st.IPURL = hostURL(in.https, in.ips[0], in.port)
		base = st.IPURL
	}
	st.QR = base + "/"
	if in.code != "" {
		st.QR = base + "/setup?code=" + in.code
	}

	switch {
	case in.live:
		st.Status = "Ready to install"
		st.Detail = "Open this address on a phone or computer to install VaporOS"
		st.Tone = brand.Installing
	case in.code != "":
		st.Status = "Almost ready"
		st.Detail = "Open this address and enter the setup code to finish setting up"
		st.Tone = brand.Installing
	case !in.gpuSupported:
		st.Status = "No supported graphics card"
		st.Detail = "VaporOS streams with an AMD Radeon graphics card"
		if in.gpuName != "" {
			st.Detail = in.gpuName + " is not supported yet; VaporOS streams with an AMD Radeon graphics card"
		}
		st.Tone = brand.Fault
	case in.virtualPending:
		st.Status = "Restart to finish setup"
		st.Detail = "Restart VaporOS from this address to switch on its virtual display"
		st.Tone = brand.RestartNeeded
	default:
		st.Status = "Ready to stream"
		st.Detail = "Open this address on a phone or computer to pair Moonlight"
		st.Tone = brand.Ready
	}
	if len(in.ips) == 0 {
		st.Status = "Waiting for the network"
		st.Detail = "Connect this computer to your router with a network cable"
		st.Tone = brand.Fault
		if !in.upSince.IsZero() && in.now.Sub(in.upSince) < bootGrace {
			st.Tone = brand.Neutral
		}
	}

	o := in.overlay
	if left, ok := o.sleepSoon(in.now); ok && !in.live {
		st.Status = "Going to sleep now"
		if mins := int((left + time.Minute - 1) / time.Minute); mins > 0 {
			st.Status = fmt.Sprintf("Going to sleep in %d min", mins)
		}
		st.Detail = "Moonlight wakes it: open Moonlight and pick this PC"
		st.Tone = brand.Asleep
	}
	if o.staged != "" && !in.live {
		st.Detail = "Update " + o.staged + " installs on the next restart"
	}
	if o.message != "" && in.now.Sub(o.messageAt) < messageTTL {
		st.Detail = o.message
	}
	if s := in.session; s != nil {
		st.Status = "Streaming to " + s.Client
		st.Detail = brand.ModeLabel(s.Mode, s.HDR)
		st.Tone = brand.Streaming
	}
	if o.updateActive(in.now) {
		u := o.update
		st.Status = "Downloading update"
		if u.Version != "" {
			st.Status += " " + u.Version
		}
		st.Detail = fmt.Sprintf("%s… %d%%", capitalize(u.Phase), u.Percent)
		st.Tone, st.Progress = brand.Updating, percent(u.Percent)
	}
	if p := o.pairing; p != nil && in.now.Sub(o.pairingAt) < progressTTL {
		st.Status = "A device wants to pair"
		if p.Name != "" {
			st.Status = p.Name + " wants to pair"
		}
		st.Detail = "Enter the PIN from Moonlight at " + base + "/pair"
		if in.code == "" {
			st.QR = base + "/pair"
		}
		st.Attention = welcome.AttentionPair
	}
	if p := o.install; p != nil {
		switch p.State {
		case "done":
			st.Status = "VaporOS is installed"
			st.Detail = orDefault(p.Message, "Remove the USB drive; the computer restarts into VaporOS")
			st.Tone, st.Progress = brand.Installing, 100
		case "failed":
			st.Status = "Installation failed"
			st.Detail = orDefault(p.Message, "Open this address for details")
			st.Tone, st.Progress = brand.Fault, 0
		case "running":
			st.Status = fmt.Sprintf("Installing VaporOS… %d%%", p.Percent)
			st.Detail = orDefault(p.Message, p.Step)
			st.Tone, st.Progress = brand.Installing, percent(p.Percent)
		}
	}
	return st
}

// percent clamps a progress figure to 0–100.
func percent(p int) int { return min(max(p, 0), 100) }

func capitalize(s string) string {
	if s == "" {
		return "Working"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
