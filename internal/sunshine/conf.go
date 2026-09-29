package sunshine

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template"
)

// Settings is the whitelisted, user-tunable part of sunshine.conf that the
// web UI edits (GET/PUT /sunshine/settings). Everything else in the file
// is fixed by VaporOS.
//
// The rendered sunshine.conf itself is where these values live between
// restarts: vosd reads them back from it before rendering again, so the
// file needs no second copy in /var/lib/vos.
type Settings struct {
	Encoder        string `json:"encoder"`
	BitrateKbpsMax int    `json:"bitrate_kbps_max"` // Sunshine's max_bitrate; 0 = whatever the client asks
	AudioSink      string `json:"audio_sink,omitempty"`
	Gamepad        string `json:"gamepad"`
}

// DefaultSettings are the reference machine's: Vulkan encode and an Xbox
// One pad, which Steam Input maps for every controller.
func DefaultSettings() Settings { return Settings{Encoder: "vulkan", Gamepad: "xone"} }

var (
	// Encoders Sunshine implements on Linux. There is deliberately no
	// "auto": vosd always names one, so the choice survives a re-render.
	encoders = []string{"vulkan", "vaapi", "software", "nvenc"}
	// Gamepad emulations current Sunshine accepts on Linux.
	gamepads = []string{"auto", "xone", "xseries", "x360", "ds4", "ds5", "switch", "generic"}
)

const maxBitrateKbps = 1_000_000

// audioSinkRe allows PulseAudio/PipeWire sink names; above all no "#"
// (a comment in sunshine.conf) and no line breaks.
var audioSinkRe = regexp.MustCompile(`^[A-Za-z0-9_.:@+-]{1,255}$`)

func (s Settings) Validate() error {
	if !slices.Contains(encoders, s.Encoder) {
		return fmt.Errorf("encoder must be one of %s", strings.Join(encoders, ", "))
	}
	if s.BitrateKbpsMax < 0 || s.BitrateKbpsMax > maxBitrateKbps {
		return fmt.Errorf("bitrate_kbps_max must be between 0 (no limit) and %d", maxBitrateKbps)
	}
	if s.AudioSink != "" && !audioSinkRe.MatchString(s.AudioSink) {
		return fmt.Errorf("audio_sink is not a valid sink name")
	}
	if !slices.Contains(gamepads, s.Gamepad) {
		return fmt.Errorf("gamepad must be one of %s", strings.Join(gamepads, ", "))
	}
	return nil
}

// settingsFromConf reads the user-tunable keys back from a rendered
// sunshine.conf; each missing or invalid value falls back to its default.
func settingsFromConf(vars map[string]string) Settings {
	s := DefaultSettings()
	try := func(apply func(*Settings)) {
		c := s
		apply(&c)
		if c.Validate() == nil {
			s = c
		}
	}
	if v, ok := vars["encoder"]; ok {
		try(func(c *Settings) { c.Encoder = v })
	}
	if v, ok := vars["max_bitrate"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			try(func(c *Settings) { c.BitrateKbpsMax = n })
		}
	}
	if v, ok := vars["audio_sink"]; ok {
		try(func(c *Settings) { c.AudioSink = v })
	}
	if v, ok := vars["gamepad"]; ok {
		try(func(c *Settings) { c.Gamepad = v })
	}
	return s
}

// sunshineOwnedKeys are keys Sunshine writes into sunshine.conf itself
// (config::persist_config_option_if_missing). They are carried over when
// vosd re-renders, or every render would undo them and every Sunshine
// start would write them back.
var sunshineOwnedKeys = []string{"gamepad_driver"}

// parseConf reads sunshine.conf the way Sunshine does (config.cpp
// parse_option): "name = value" per line, "#" starts a comment anywhere,
// and a value starting with "[" runs to its matching "]", across lines.
// Later duplicates are ignored, as std::unordered_map::emplace does.
func parseConf(data []byte) map[string]string {
	s := string(data)
	vars := map[string]string{}
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == '\v' }
	pos := 0
	for pos < len(s) {
		for pos < len(s) && isSpace(s[pos]) {
			pos++
		}
		if pos >= len(s) {
			break
		}
		endl := pos
		for endl < len(s) && s[endl] != '\n' && s[endl] != '\r' {
			endl++
		}
		line := s[pos:endl]
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimRight(line, " \t\r\n\f\v")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			pos = endl
			continue
		}
		name := strings.TrimRight(line[:eq], " \t")
		value := strings.TrimLeft(line[eq+1:], " \t")
		if strings.HasPrefix(value, "[") {
			// The list may continue on the following lines.
			start := pos + eq + 1 + (len(line[eq+1:]) - len(value))
			depth, i := 1, start+1
			for i < len(s) && depth > 0 {
				switch s[i] {
				case '[':
					depth++
				case ']':
					depth--
				}
				i++
			}
			if depth > 0 {
				pos = len(s) // unterminated list: Sunshine drops it too
				continue
			}
			value, endl = s[start:i], i
		}
		if name != "" && value != "" {
			if _, dup := vars[name]; !dup {
				vars[name] = value
			}
		}
		pos = endl
	}
	return vars
}

//go:embed sunshine.conf.tmpl
var defaultTemplate string

// confData is what sunshine.conf.tmpl renders from. An image may ship its
// own /usr/share/vos/sunshine.conf.tmpl using these fields.
type confData struct {
	Encoder     string // one of the Linux encoders
	AdapterName string // /dev/dri/renderD*, the GPU that drives OutputName
	OutputName  string // KMS connector name of the virtual display ("DP-1"), or ""
	MaxBitrate  int    // kbps, 0 = no cap
	AudioSink   string // "" = the default sink
	Gamepad     string
	PrepCmd     string // JSON list for global_prep_cmd
}

// sessionPrepCmd runs `vos session begin|end` around every stream, as the
// gaming user (Sunshine's), which talks to vosd over its socket.
var sessionPrepCmd = func() string {
	b, _ := json.Marshal([]struct {
		Do       string `json:"do"`
		Undo     string `json:"undo"`
		Elevated bool   `json:"elevated"`
	}{{Do: "/usr/bin/vos session begin", Undo: "/usr/bin/vos session end"}})
	return string(b)
}()

// loadTemplate returns the image's template, or the built-in one when the
// image has none or it does not parse.
func loadTemplate() *template.Template {
	builtin := template.Must(template.New("sunshine.conf").Option("missingkey=error").Parse(defaultTemplate))
	b, err := os.ReadFile(templatePath())
	if err != nil {
		return builtin
	}
	t, err := template.New("sunshine.conf").Option("missingkey=error").Parse(string(b))
	if err != nil {
		log.Printf("sunshine: %s: %v (using the built-in template)", templatePath(), err)
		return builtin
	}
	return t
}

// renderConf produces sunshine.conf. prev is the current file, from which
// Sunshine's own keys are carried over. A template that fails to execute
// (an image template expecting other fields) falls back to the built-in
// one, so Sunshine always gets a working configuration.
func renderConf(t *template.Template, d confData, prev map[string]string) []byte {
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		log.Printf("sunshine: rendering sunshine.conf: %v (using the built-in template)", err)
		buf.Reset()
		template.Must(template.New("sunshine.conf").Parse(defaultTemplate)).Execute(&buf, d)
	}
	out := buf.Bytes()
	rendered := parseConf(out)
	var carried []string
	for _, k := range sunshineOwnedKeys {
		if v, ok := prev[k]; ok {
			if _, set := rendered[k]; !set {
				carried = append(carried, k+" = "+v)
			}
		}
	}
	if len(carried) > 0 {
		sort.Strings(carried)
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, "\n# Written by Sunshine itself; kept when vosd renders this file.\n"...)
		out = append(out, strings.Join(carried, "\n")+"\n"...)
	}
	return out
}

var connectorRe = regexp.MustCompile(`^[A-Za-z]+(-[A-Za-z])?-[0-9]+$`)

// outputName turns the configured virtual connector into Sunshine's
// output_name for capture = kms.
//
// Current Sunshine (kmsgrab.cpp map_display_name_to_monitor_index) takes
// either a connector name, matched against
// drmModeGetConnectorTypeName(type) + "-" + connector_type_id, which is
// exactly the kernel's connector name ("DP-1", "HDMI-A-1"), or a bare
// number, meaning the n-th plane with a framebuffer at probe time. The
// number shifts whenever another output (a monitor showing the welcome
// screen) is lit, so VaporOS always uses the name. sysfs spells connectors
// "card1-DP-1"; the card prefix is dropped.
func outputName(connector string) string {
	c := strings.TrimSpace(connector)
	if rest, ok := strings.CutPrefix(c, "card"); ok {
		if i := strings.IndexByte(rest, '-'); i > 0 && isDigits(rest[:i]) {
			c = rest[i+1:]
		}
	}
	if !connectorRe.MatchString(c) {
		return ""
	}
	return c
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

const defaultAdapter = "/dev/dri/renderD128"

// adapterFor returns the render node of the GPU that owns connector, so
// capture and encode happen on the card driving the virtual display. With
// one GPU that is renderD128.
func adapterFor(sysDRM, connector string) string {
	if connector == "" {
		return defaultAdapter
	}
	cards, _ := filepath.Glob(filepath.Join(sysDRM, "card*-"+connector))
	sort.Strings(cards)
	for _, c := range cards {
		card, _, ok := strings.Cut(filepath.Base(c), "-")
		if !ok {
			continue
		}
		nodes, _ := filepath.Glob(filepath.Join(sysDRM, card, "device", "drm", "renderD*"))
		sort.Strings(nodes)
		if len(nodes) > 0 {
			return "/dev/dri/" + filepath.Base(nodes[0])
		}
	}
	return defaultAdapter
}
