package display

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/display/edid"
	"github.com/jasperaelvoet/vaporos/internal/display/welcome"
	"github.com/jasperaelvoet/vaporos/internal/events"
)

// How long transient notices stay on the welcome screen without a refresh.
const (
	progressTTL = 2 * time.Minute // update/pairing progress without news
	messageTTL  = 5 * time.Minute
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
	case in.code != "":
		st.Status = "Almost ready"
		st.Detail = "Open this address and enter the setup code to finish setting up"
	case !in.gpuSupported:
		st.Status = "No supported graphics card"
		st.Detail = "VaporOS streams with an AMD Radeon graphics card"
		if in.gpuName != "" {
			st.Detail = in.gpuName + " is not supported yet; VaporOS streams with an AMD Radeon graphics card"
		}
	case in.virtualPending:
		st.Status = "Restart to finish setup"
		st.Detail = "Restart VaporOS from this address to switch on its virtual display"
	default:
		st.Status = "Ready to stream"
		st.Detail = "Open this address on a phone or computer to pair Moonlight"
	}
	if len(in.ips) == 0 {
		st.Status = "Waiting for the network"
		st.Detail = "Connect this computer to your router with a network cable"
	}

	o := in.overlay
	if o.staged != "" && !in.live {
		st.Detail = "Update " + o.staged + " installs on the next restart"
	}
	if o.message != "" && in.now.Sub(o.messageAt) < messageTTL {
		st.Detail = o.message
	}
	if s := in.session; s != nil {
		st.Status = "Streaming to " + s.Client
		st.Detail = modeLabel(s.Mode, s.HDR)
	}
	if o.updateActive(in.now) {
		u := o.update
		st.Status = "Downloading update"
		if u.Version != "" {
			st.Status += " " + u.Version
		}
		st.Detail = fmt.Sprintf("%s… %d%%", capitalize(u.Phase), u.Percent)
	}
	if p := o.pairing; p != nil && in.now.Sub(o.pairingAt) < progressTTL {
		st.Status = "A device wants to pair"
		if p.Name != "" {
			st.Status = p.Name + " wants to pair"
		}
		st.Detail = "Enter the PIN from Moonlight at " + base + "/pair"
	}
	if p := o.install; p != nil {
		switch p.State {
		case "done":
			st.Status = "VaporOS is installed"
			st.Detail = orDefault(p.Message, "Remove the USB drive; the computer restarts into VaporOS")
		case "failed":
			st.Status = "Installation failed"
			st.Detail = orDefault(p.Message, "Open this address for details")
		case "running":
			st.Status = fmt.Sprintf("Installing VaporOS… %d%%", p.Percent)
			st.Detail = orDefault(p.Message, p.Step)
		}
	}
	return st
}

// modeLabel spells a "WxH@R" mode for people: "3840 × 2160 · 120 Hz · HDR".
func modeLabel(mode string, hdr bool) string {
	var parts []string
	if md, err := edid.ParseMode(mode); err == nil {
		parts = append(parts, fmt.Sprintf("%d × %d", md.W, md.H), fmt.Sprintf("%d Hz", md.Refresh))
	} else if mode != "" {
		parts = append(parts, mode)
	}
	if hdr {
		parts = append(parts, "HDR")
	}
	return strings.Join(parts, " · ")
}

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
