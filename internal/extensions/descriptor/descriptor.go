// Package descriptor is the extension descriptor, extensions/<id>/extension.json
// (docs/CONTRACTS.md "Extensions"). The build reads it to make the image and
// checks the image's content against it; vosd reads the copy the build ships
// in the root (/usr/share/vos/extensions/<id>.json) to show, install and run
// the extension. Unknown fields are an error, so a typo never passes as an
// ignored key.
package descriptor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// Schema is the descriptor format this vos reads.
const Schema = 1

// MaxSize bounds a descriptor file.
const MaxSize = 256 << 10

// Categories.
const (
	Runtime = "runtime" // something other extensions and Steam use (Proton)
	System  = "system"  // a system service
	App     = "app"     // something you start from Steam
)

// Permission categories the build derives from an image's content. An
// extension declares the ones it expects; the build fails on any difference,
// so a package update that starts shipping, say, a udev rule is a reviewed
// change rather than a silent one.
const (
	PermService     = "service"      // a system unit (root unless it says User= or DynamicUser=)
	PermUserService = "user-service" // a unit for vapor's user manager
	PermUdev        = "udev"         // udev rules
	PermSysctl      = "sysctl"       // kernel settings (sysctl.d)
	PermModules     = "modules"      // kernel modules loaded at boot (modules-load.d)
	PermPolkit      = "polkit"       // polkit rules
	PermDBus        = "dbus"         // D-Bus system policy or services
	PermCompatTool  = "compat-tool"  // a Steam compatibility tool
)

// Permissions lists every category, in the order the UI shows them.
var Permissions = []string{PermService, PermUserService, PermUdev, PermSysctl, PermModules, PermPolkit, PermDBus, PermCompatTool}

// Descriptor is extension.json.
type Descriptor struct {
	Schema   int      `json:"schema"`
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Summary  string   `json:"summary"`
	Category string   `json:"category"`
	Core     bool     `json:"core,omitempty"`
	Upstream Upstream `json:"upstream"`
	Caveats  []string `json:"caveats,omitempty"`
	Copy     Copy     `json:"copy,omitempty"`

	Packages []string `json:"packages,omitempty"`
	Fetch    []Fetch  `json:"fetch,omitempty"`
	// Strip lists package files the image leaves out (paths under usr/);
	// each must exist, so a package that drops one is noticed.
	Strip []string `json:"strip,omitempty"`
	// ELFExempt lists directories (under usr/) whose programs run in a
	// container rather than on the host, so the build does not resolve
	// their libraries against the base.
	ELFExempt []string `json:"elf_exempt,omitempty"`

	Requires  []string `json:"requires,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
	Provides  []string `json:"provides,omitempty"`

	Permissions   []string       `json:"permissions,omitempty"`
	Services      []Service      `json:"services,omitempty"`
	ModuleOptions []ModuleOption `json:"module_options,omitempty"`
	Network       *Network       `json:"network,omitempty"`
	Web           *Web           `json:"web,omitempty"`
	Steam         *Steam         `json:"steam,omitempty"`
	Data          []Data         `json:"data,omitempty"`
	Shares        []Share        `json:"shares,omitempty"`
	Downloads     []Download     `json:"downloads,omitempty"`
	Settings      []Setting      `json:"settings,omitempty"`
	Actions       []Action       `json:"actions,omitempty"`

	// Build is filled in by the build in the copy it ships; a source
	// descriptor must not have it.
	Build *Build `json:"build,omitempty"`
}

type Upstream struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	License string `json:"license"`
}

// Copy is the text of the install and remove dialogs (plain voice, see
// design/voice.md).
type Copy struct {
	Install string `json:"install,omitempty"`
	Remove  string `json:"remove,omitempty"`
}

// Fetch is a file the build downloads into usr/lib/vos/ext/<id>/<dest>,
// pinned by sha256. With extract, url is a tar archive and extract the
// member to take from it; license_file is then the archive member holding
// the licence text, which the build installs into usr/share/licenses/<id>/.
type Fetch struct {
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	License     string `json:"license"`
	Extract     string `json:"extract,omitempty"`
	LicenseFile string `json:"license_file,omitempty"`
	Dest        string `json:"dest"`
}

type Service struct {
	Unit  string `json:"unit"`
	Scope string `json:"scope"` // "system" | "user"
}

// ModuleOption is a kernel module parameter the extension may set at boot
// (through /run/modprobe.d), from one of its settings.
type ModuleOption struct {
	Module  string `json:"module"`
	Param   string `json:"param"`
	Setting string `json:"setting"`
}

type Network struct {
	Ports []Port `json:"ports"`
}

// Port is a port the firewall opens to the local network while the
// extension is mounted. "proxied" ports are served by vosd, which forwards
// to Upstream on localhost; "lan" ports belong to the extension's service.
type Port struct {
	Proto    string `json:"proto"` // "tcp" | "udp"
	Port     int    `json:"port"`
	Mode     string `json:"mode"` // "proxied" | "lan"
	Upstream string `json:"upstream,omitempty"`
}

// Web is a web UI the control center links to: vosd serves it on Port.
type Web struct {
	Port  int    `json:"port"`
	Label string `json:"label"`
}

type Steam struct {
	DefaultCompatTool string     `json:"default_compat_tool,omitempty"`
	ForceCompatTool   []uint32   `json:"force_compat_tool,omitempty"`
	CompatTool        string     `json:"compat_tool,omitempty"` // the tool force_compat_tool and shortcuts use
	Hooks             []Hook     `json:"hooks,omitempty"`
	Shortcuts         []Shortcut `json:"shortcuts,omitempty"`
}

// Hook wraps the launch of Steam apps with the extension's launch hook.
// The hooks of several extensions on one app run in catalog order, so a
// descriptor has no say in it.
type Hook struct {
	Apps []uint32 `json:"apps"`
}

// Shortcut is a non-Steam game the extension adds to Steam.
type Shortcut struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	CompatTool bool   `json:"compat_tool,omitempty"` // run it with Steam.CompatTool
	Art        string `json:"art,omitempty"`         // directory under usr/lib/vos/ext/<id>/
}

// Data is a place the extension keeps state: "system" under
// /var/lib/vos/ext/data/<id>, "home" in vapor's home, "library" on a disk
// the user picks.
type Data struct {
	Name      string   `json:"name"`
	Where     string   `json:"where"`
	MinFreeGB int      `json:"min_free_gb,omitempty"`
	FS        []string `json:"fs,omitempty"`
}

// Share names a Steam game whose data the extension uses as its own.
type Share struct {
	SteamApp uint32 `json:"steam_app"`
}

// Download is something the extension fetches on the box, said plainly.
type Download struct {
	What     string `json:"what"`
	From     string `json:"from"`
	Checked  string `json:"checked"` // "pinned" | "publisher-hash" | "none"
	RunsCode bool   `json:"runs_code,omitempty"`
	When     string `json:"when"` // "install" | "update" | "launch"
}

type Setting struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"` // "bool" | "choice" | "disk"
	Label   string   `json:"label"`
	Help    string   `json:"help,omitempty"`
	Restart bool     `json:"restart,omitempty"`
	Choices []string `json:"choices,omitempty"`
	Default any      `json:"default,omitempty"`
}

type Action struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Confirm *Confirm `json:"confirm,omitempty"`
	RunAs   string   `json:"run_as,omitempty"` // "vapor" (default) | "root"
}

type Confirm struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Button string `json:"button"`
	Tone   string `json:"tone,omitempty"` // "" | "danger"
}

// Build is what the build learned making the image.
type Build struct {
	Size        int64    `json:"size"`
	Packages    []string `json:"packages,omitempty"` // "name version", what the image holds
	Permissions []string `json:"permissions"`        // verified against the content
	RunsAsRoot  bool     `json:"runs_as_root,omitempty"`
}

var (
	packageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/[a-z0-9@._+-]+$`)
	unitRe    = regexp.MustCompile(`^[A-Za-z0-9@._-]+\.(service|timer|socket|path)$`)
	identRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	capRe     = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}$`)
	toolRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	licenseRe = regexp.MustCompile(`^[A-Za-z0-9.+ ()-]{1,64}$`)
	hostRe    = regexp.MustCompile(`^[a-z0-9.-]+\.[a-z]{2,}$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Load reads and validates a descriptor file.
func Load(file string) (*Descriptor, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxSize {
		return nil, fmt.Errorf("%s: larger than %d bytes", file, MaxSize)
	}
	d, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return d, nil
}

// Parse decodes and validates a descriptor.
func Parse(b []byte) (*Descriptor, error) {
	var d Descriptor
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after the JSON object")
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// Validate checks every field.
func (d *Descriptor) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	text := func(field, s string, max int, required bool) {
		switch {
		case s == "" && required:
			bad("%s is required", field)
		case utf8.RuneCountInString(s) > max:
			bad("%s is longer than %d characters", field, max)
		case strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }):
			bad("%s contains control characters", field)
		}
	}

	if d.Schema != Schema {
		bad("schema is %d, this vos reads %d", d.Schema, Schema)
	}
	if !manifest.ValidExtensionID(d.ID) {
		bad("invalid id %q", d.ID)
	}
	text("name", d.Name, 40, true)
	text("summary", d.Summary, 140, true)
	if !slices.Contains([]string{Runtime, System, App}, d.Category) {
		bad("category must be runtime, system or app, not %q", d.Category)
	}
	text("upstream.name", d.Upstream.Name, 60, true)
	if !httpsURL(d.Upstream.URL) {
		bad("upstream.url must be an https URL")
	}
	if !licenseRe.MatchString(d.Upstream.License) {
		bad("upstream.license is missing or malformed")
	}
	for i, c := range d.Caveats {
		text(fmt.Sprintf("caveats[%d]", i), c, 200, true)
	}
	text("copy.install", d.Copy.Install, 400, false)
	text("copy.remove", d.Copy.Remove, 400, false)

	for _, p := range d.Packages {
		if !packageRe.MatchString(p) {
			bad("package %q must be repo/name", p)
		}
	}
	dests := map[string]bool{}
	for i, f := range d.Fetch {
		if !httpsURL(f.URL) {
			bad("fetch[%d].url must be an https URL", i)
		}
		if !sha256Re.MatchString(f.SHA256) {
			bad("fetch[%d].sha256 must be 64 lowercase hex digits", i)
		}
		if !licenseRe.MatchString(f.License) {
			bad("fetch[%d].license is missing or malformed", i)
		}
		if f.Extract != "" && !cleanRel(f.Extract) {
			bad("fetch[%d].extract must be a relative path inside the archive", i)
		}
		if f.LicenseFile != "" && (f.Extract == "" || !cleanRel(f.LicenseFile)) {
			bad("fetch[%d].license_file must be a relative path inside the archive named by extract", i)
		}
		if !cleanRel(f.Dest) || dests[f.Dest] {
			bad("fetch[%d].dest must be a unique relative path", i)
		}
		dests[f.Dest] = true
	}
	for _, p := range d.Strip {
		if !cleanRel(p) || !strings.HasPrefix(p, "usr/") {
			bad("strip %q must be a path under usr/", p)
		}
	}
	for _, p := range d.ELFExempt {
		if !cleanRel(p) || !strings.HasPrefix(p, "usr/") {
			bad("elf_exempt %q must be a directory under usr/", p)
		}
	}

	for _, r := range d.Requires {
		if !manifest.ValidExtensionID(r) || r == d.ID {
			bad("requires %q is not another extension's id", r)
		}
	}
	for _, c := range d.Conflicts {
		if !capRe.MatchString(c) || c == d.ID {
			bad("conflicts %q is not an extension id or capability", c)
		}
	}
	for _, p := range d.Provides {
		if !capRe.MatchString(p) {
			bad("provides %q is not a capability name", p)
		}
	}

	seenPerm := map[string]bool{}
	for _, p := range d.Permissions {
		if !slices.Contains(Permissions, p) || seenPerm[p] {
			bad("unknown or repeated permission %q", p)
		}
		seenPerm[p] = true
	}
	for i, s := range d.Services {
		if !unitRe.MatchString(s.Unit) {
			bad("services[%d].unit %q is not a unit name", i, s.Unit)
		}
		switch s.Scope {
		case "system":
			if !seenPerm[PermService] {
				bad("services[%d] is a system unit, so permissions must list %q", i, PermService)
			}
		case "user":
			if !seenPerm[PermUserService] {
				bad("services[%d] is a user unit, so permissions must list %q", i, PermUserService)
			}
		default:
			bad("services[%d].scope must be system or user", i)
		}
	}

	settings := map[string]Setting{}
	for i, s := range d.Settings {
		if !identRe.MatchString(s.Key) || settings[s.Key].Key != "" {
			bad("settings[%d].key %q is not a unique identifier", i, s.Key)
		}
		settings[s.Key] = s
		text(fmt.Sprintf("settings[%d].label", i), s.Label, 60, true)
		text(fmt.Sprintf("settings[%d].help", i), s.Help, 200, false)
		switch s.Type {
		case "bool", "disk":
			if len(s.Choices) > 0 {
				bad("settings[%d] has choices but is %s", i, s.Type)
			}
		case "choice":
			if len(s.Choices) < 2 {
				bad("settings[%d] is a choice with fewer than two choices", i)
			}
			for _, c := range s.Choices {
				if !identRe.MatchString(c) {
					bad("settings[%d] choice %q is not an identifier", i, c)
				}
			}
		default:
			bad("settings[%d].type must be bool, choice or disk", i)
		}
	}
	for i, m := range d.ModuleOptions {
		if !identRe.MatchString(m.Module) || !identRe.MatchString(m.Param) {
			bad("module_options[%d] needs a module and param name", i)
		}
		if s, ok := settings[m.Setting]; !ok || !s.Restart {
			bad("module_options[%d].setting %q must be a setting that needs a restart", i, m.Setting)
		}
	}

	if d.Network != nil {
		for i, p := range d.Network.Ports {
			if p.Proto != "tcp" && p.Proto != "udp" {
				bad("network.ports[%d].proto must be tcp or udp", i)
			}
			if p.Port < 1024 || p.Port > 65535 {
				bad("network.ports[%d].port must be 1024-65535", i)
			}
			switch p.Mode {
			case "proxied":
				if p.Proto != "tcp" || !loopbackUpstream(p.Upstream) {
					bad("network.ports[%d] is proxied, so it is tcp with an upstream on 127.0.0.1", i)
				}
			case "lan":
				if p.Upstream != "" {
					bad("network.ports[%d] is lan and has no upstream", i)
				}
			default:
				bad("network.ports[%d].mode must be proxied or lan", i)
			}
		}
	}
	if d.Web != nil {
		text("web.label", d.Web.Label, 40, true)
		ok := false
		if d.Network != nil {
			for _, p := range d.Network.Ports {
				ok = ok || (p.Port == d.Web.Port && p.Mode == "proxied")
			}
		}
		if !ok {
			bad("web.port %d must be one of network.ports, proxied", d.Web.Port)
		}
	}

	if s := d.Steam; s != nil {
		if s.DefaultCompatTool != "" && (!d.Core || !toolRe.MatchString(s.DefaultCompatTool)) {
			bad("only a core extension sets steam.default_compat_tool, and it must be a tool name")
		}
		if s.CompatTool != "" && !toolRe.MatchString(s.CompatTool) {
			bad("steam.compat_tool %q is not a tool name", s.CompatTool)
		}
		if len(s.ForceCompatTool) > 0 && s.CompatTool == "" {
			bad("steam.force_compat_tool needs steam.compat_tool")
		}
		for _, a := range s.ForceCompatTool {
			if a == 0 {
				bad("steam.force_compat_tool lists app 0")
			}
		}
		for i, h := range s.Hooks {
			if len(h.Apps) == 0 || slices.Contains(h.Apps, 0) {
				bad("steam.hooks[%d] needs apps, none of them 0", i)
			}
		}
		keys := map[string]bool{}
		for i, sc := range s.Shortcuts {
			if !nameRe.MatchString(sc.Key) || keys[sc.Key] {
				bad("steam.shortcuts[%d].key %q is not a unique name", i, sc.Key)
			}
			keys[sc.Key] = true
			text(fmt.Sprintf("steam.shortcuts[%d].name", i), sc.Name, 60, true)
			if sc.CompatTool && s.CompatTool == "" {
				bad("steam.shortcuts[%d] runs with steam.compat_tool, which is not set", i)
			}
			if sc.Art != "" && !cleanRel(sc.Art) {
				bad("steam.shortcuts[%d].art must be a relative path", i)
			}
		}
	}

	dataNames := map[string]bool{}
	for i, x := range d.Data {
		if !nameRe.MatchString(x.Name) || dataNames[x.Name] {
			bad("data[%d].name %q is not a unique name", i, x.Name)
		}
		dataNames[x.Name] = true
		if !slices.Contains([]string{"system", "home", "library"}, x.Where) {
			bad("data[%d].where must be system, home or library", i)
		}
		if x.MinFreeGB < 0 {
			bad("data[%d].min_free_gb is negative", i)
		}
		for _, fs := range x.FS {
			if !slices.Contains([]string{"ext4", "btrfs", "xfs", "f2fs", "ntfs3"}, fs) {
				bad("data[%d].fs %q is not a library filesystem", i, fs)
			}
		}
	}
	for i, s := range d.Shares {
		if s.SteamApp == 0 {
			bad("shares[%d].steam_app is 0", i)
		}
	}
	for i, dl := range d.Downloads {
		text(fmt.Sprintf("downloads[%d].what", i), dl.What, 120, true)
		if !hostRe.MatchString(dl.From) {
			bad("downloads[%d].from must be a host name", i)
		}
		if !slices.Contains([]string{"pinned", "publisher-hash", "none"}, dl.Checked) {
			bad("downloads[%d].checked must be pinned, publisher-hash or none", i)
		}
		if !slices.Contains([]string{"install", "update", "launch"}, dl.When) {
			bad("downloads[%d].when must be install, update or launch", i)
		}
	}
	actions := map[string]bool{}
	for i, a := range d.Actions {
		if !nameRe.MatchString(a.Name) || actions[a.Name] {
			bad("actions[%d].name %q is not a unique name", i, a.Name)
		}
		actions[a.Name] = true
		text(fmt.Sprintf("actions[%d].label", i), a.Label, 40, true)
		if a.RunAs != "" && a.RunAs != "vapor" && a.RunAs != "root" {
			bad("actions[%d].run_as must be vapor or root", i)
		}
		if c := a.Confirm; c != nil {
			text(fmt.Sprintf("actions[%d].confirm.title", i), c.Title, 80, true)
			text(fmt.Sprintf("actions[%d].confirm.body", i), c.Body, 300, true)
			text(fmt.Sprintf("actions[%d].confirm.button", i), c.Button, 30, true)
			if c.Tone != "" && c.Tone != "danger" {
				bad("actions[%d].confirm.tone must be empty or danger", i)
			}
		}
	}
	return errors.Join(errs...)
}

// ValidateSource checks a descriptor as it is in the repository: valid, and
// without the build's own section.
func (d *Descriptor) ValidateSource() error {
	if d.Build != nil {
		return errors.New("build is written by the build, not by hand")
	}
	return nil
}

// Setting returns the setting with key.
func (d *Descriptor) Setting(key string) (Setting, bool) {
	for _, s := range d.Settings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// RunsAsRoot reports whether the build found a root service in the image.
func (d *Descriptor) RunsAsRoot() bool { return d.Build != nil && d.Build.RunsAsRoot }

// HasModuleOptions reports whether the extension can set kernel module
// parameters.
func (d *Descriptor) HasModuleOptions() bool { return len(d.ModuleOptions) > 0 }

func httpsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

func cleanRel(p string) bool {
	return p != "" && !path.IsAbs(p) && path.Clean(p) == p && p != "." && !strings.HasPrefix(p, "../") && p != ".."
}

func loopbackUpstream(s string) bool {
	host, port, ok := strings.Cut(s, ":")
	if !ok || (host != "127.0.0.1") {
		return false
	}
	n := 0
	for _, r := range port {
		if r < '0' || r > '9' {
			return false
		}
		n = n*10 + int(r-'0')
		if n > 65535 {
			return false
		}
	}
	return n >= 1024
}
