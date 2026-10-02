package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// StateInstalling is a card's state while its image downloads or seals or
// its helper sets it up, or while it waits for the pass that fetches or
// proposes it (GET /extensions).
const StateInstalling = "installing"

// Document is GET /extensions and the extensions.state event
// (docs/CONTRACTS.md "HTTP API").
type Document struct {
	Extensions []ExtensionDoc `json:"extensions"`
	Restart    RestartDoc     `json:"restart"`
	SkipOnce   bool           `json:"skip_once"` // the next start leaves the extensions out
}

// RestartDoc says whether a restart would try a change to the extensions
// (the /status restart kind "extensions"), whether VaporOS may still do it
// by itself once the PC is idle, and what it would do, in words.
type RestartDoc struct {
	Needed bool   `json:"needed"`
	Auto   bool   `json:"auto"`
	Reason string `json:"reason"`
}

// ExtensionDoc is one card.
type ExtensionDoc struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Summary       string              `json:"summary"`
	Category      string              `json:"category"`
	Core          bool                `json:"core"`
	Upstream      descriptor.Upstream `json:"upstream"`
	Caveats       []string            `json:"caveats"`
	State         string              `json:"state"`
	Wanted        bool                `json:"wanted"`
	Mounted       bool                `json:"mounted"`
	Size          int64               `json:"size"`
	Progress      *Progress           `json:"progress"`
	Reason        string              `json:"reason"`
	Permissions   []string            `json:"permissions"`
	RunsAsRoot    bool                `json:"runs_as_root"`
	Downloads     []DownloadDoc       `json:"downloads"`
	Settings      []SettingDoc        `json:"settings"`
	Actions       []ActionDoc         `json:"actions"`
	Web           *WebDoc             `json:"web"`
	WebRunning    bool                `json:"web_running"` // vosd serves its web UI now
	Status        []StatusLine        `json:"status"`
	Copy          CopyDoc             `json:"copy"`
	Requires      []string            `json:"requires"`
	RequiredBy    []string            `json:"required_by"`
	NeedsPassword bool                `json:"needs_password"`
	ModuleOptions bool                `json:"module_options"`
	Steam         *SteamDoc           `json:"steam"`
}

// SteamDoc is what an extension changes in Steam, by the names Steam shows
// (an app's name from its installed appmanifest, "" when it is not
// installed): the compatibility tool it runs its forced apps and
// shortcuts with, the apps it forces that tool on, the apps whose launch
// it hooks and the shortcuts it adds.
type SteamDoc struct {
	CompatTool string        `json:"compat_tool"`
	Forces     []SteamAppDoc `json:"forces"`
	Hooks      []SteamAppDoc `json:"hooks"`
	Shortcuts  []ShortcutDoc `json:"shortcuts"`
}

type SteamAppDoc struct {
	App  uint32 `json:"app"`
	Name string `json:"name"`
}

type ShortcutDoc struct {
	Name string `json:"name"`
}

type DownloadDoc struct {
	What     string `json:"what"`
	From     string `json:"from"`
	Checked  string `json:"checked"`
	RunsCode bool   `json:"runs_code"`
	When     string `json:"when"`
}

type SettingDoc struct {
	Key           string   `json:"key"`
	Type          string   `json:"type"`
	Label         string   `json:"label"`
	Help          string   `json:"help"`
	Restart       bool     `json:"restart"`
	Choices       []string `json:"choices"`
	Value         any      `json:"value"`
	NeedsPassword bool     `json:"needs_password"` // it feeds kernel module options: changing it takes the admin password
	Required      bool     `json:"required"`       // a disk setting adding it asks for: "" is no drive picked yet
}

type ActionDoc struct {
	Name    string      `json:"name"`
	Label   string      `json:"label"`
	Confirm *ConfirmDoc `json:"confirm"`
}

type ConfirmDoc struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Button string `json:"button"`
	Tone   string `json:"tone"`
}

type WebDoc struct {
	Port  int    `json:"port"`
	Label string `json:"label"`
}

type CopyDoc struct {
	Install string `json:"install"`
	Remove  string `json:"remove"`
}

// Why a card needs attention, as it says (design/voice.md: what happened,
// then what to do).
const (
	blockedText      = "It kept VaporOS from starting properly, so VaporOS started without it. Try again, or remove it."
	notInVersionText = "This version of VaporOS does not have it. It comes back with an update that has it."
	startedOffText   = "VaporOS started without extensions this time. Restart to start them again."
	notTriedText     = "VaporOS couldn't get ready to start it this time. Restart to try again, or remove it."
)

// installNote is a card's reason when its helper's Install failed (the
// error itself goes to the log): core cannot be removed.
func installNote(name string, core bool) string {
	if core {
		return "Setting up " + name + " didn't finish. Try again."
	}
	return "Setting up " + name + " didn't finish. Try again, or remove it."
}

// pickDriveNote is a card's reason while a required disk setting has no
// drive, so its helper does not set it up.
func pickDriveNote(name string) string {
	return "Pick a game drive for " + name + ", then select Try again."
}

// removeNote is a card's reason when its helper's Remove failed.
func removeNote(name string) string {
	return "Removing " + name + " didn't finish. Try removing it again."
}

// needsText is the reason of a wanted extension whose requirement's image
// cannot be had.
func needsText(name string) string {
	return "It needs " + name + ", which VaporOS can't add right now. See " + name + " for why."
}

var skipText = map[string]string{
	store.SkipRequires:     "An extension it needs did not start. Restart to try again, or remove it.",
	store.SkipMissing:      "Its files were missing when VaporOS started. VaporOS downloads them again.",
	store.SkipSize:         damagedText,
	store.SkipFSVerity:     damagedText,
	store.SkipUnproven:     "It has not been started yet. Restart to start it.",
	store.SkipMount:        "It could not be started. Restart to try again, or remove it.",
	store.SkipNoUsr:        "It could not be started. Restart to try again, or remove it.",
	store.SkipOverlay:      "It could not be started. Restart to try again, or remove it.",
	store.SkipNotInCatalog: notInVersionText,
}

// helperStatusWait bounds one helper's Status while the document is built.
const helperStatusWait = time.Second

// Document builds GET /extensions: the booted catalog's extensions (then
// wanted ones it lacks) with what their shipped descriptors say, their
// state after the last pass and the changes since, their settings and
// their helpers' status lines. A web UI is listed whether or not it runs:
// vosd serves it only while its extension is mounted.
func (s *Service) Document(ctx context.Context) Document {
	st := s.Status()
	s.mu.Lock()
	v := s.view
	notes, tries := maps.Clone(s.cc.notes), maps.Clone(s.cc.tries)
	running := map[string]bool{}
	for id := range s.cc.installing {
		running[id] = true
	}
	s.mu.Unlock()
	wanted := v.wanted
	if w, err := store.Wanted(); err == nil {
		wanted = w
	}
	want := wantSet(v.cat, wanted)
	byID := map[string]ExtensionStatus{}
	for _, x := range st.Extensions {
		byID[x.ID] = x
	}
	names := s.appNames(s.steamApps(st.Extensions))
	doc := Document{Extensions: []ExtensionDoc{}, SkipOnce: skipOnce()}
	var adding, removing, changing []string
	for _, x := range st.Extensions {
		e, inCat := v.cat.Get(x.ID)
		d := s.desc(x.ID)
		xd := s.card(ctx, x.ID, e, inCat, d, names)
		xd.Wanted = x.Core || slices.Contains(wanted, x.ID)
		xd.Mounted, xd.Progress = x.Mounted, x.Progress
		xd.RequiredBy = requiredBy(v.cat, want, x.ID)
		xd.NeedsPassword = s.needsPassword(v.cat, want, x.ID)
		f := cardFacts{inWant: want[x.ID] || xd.Wanted, note: notes[x.ID], lines: xd.Status}
		// Also after a failed try while another comes by itself.
		f.settingUp = running[x.ID] || (x.Mounted && f.inWant && d != nil &&
			tries[x.ID] < maxHelperInstalls && !isInstalled(x.ID))
		if f.inWant && !x.Mounted {
			f.blocker = s.blocker(v.cat, byID, x.ID)
		}
		xd.State, xd.Reason = s.cardState(x, &v, f)
		if xd.State == StateRestartNeeded {
			switch {
			case !x.Mounted:
				adding = append(adding, xd.Name)
			case !f.inWant:
				removing = append(removing, xd.Name)
			default:
				changing = append(changing, xd.Name)
			}
		}
		doc.Extensions = append(doc.Extensions, xd)
	}
	// The cards wait for a restart with skip-once too, but the next start
	// mounts nothing: the change waits for the restart after that, which
	// the reason says, and VaporOS does not restart by itself for it.
	doc.Restart.Needed = st.RestartNeeded
	if doc.Restart.Needed {
		doc.Restart.Reason = restartReason(doc.SkipOnce, adding, removing, changing)
		doc.Restart.Auto = s.autoAllowed(&v)
	}
	return doc
}

// needsPassword reports whether adding id takes the admin password again:
// it, or a requirement not wanted yet (directly or not), runs as root or
// sets kernel module options.
func (s *Service) needsPassword(cat *catalog.Catalog, want map[string]bool, id string) bool {
	if passwordFor(s.desc(id)) {
		return true
	}
	for _, r := range cat.Closure([]string{id}) {
		if r != id && !want[r] && passwordFor(s.desc(r)) {
			return true
		}
	}
	return false
}

func passwordFor(d *descriptor.Descriptor) bool {
	return d != nil && (d.RunsAsRoot() || d.HasModuleOptions())
}

// blocker is the name of a requirement of id (directly or not) that is
// not running and whose image cannot be had, or "".
func (s *Service) blocker(cat *catalog.Catalog, byID map[string]ExtensionStatus, id string) string {
	for _, r := range cat.Closure([]string{id}) {
		if x := byID[r]; r != id && x.Error != "" && !x.Mounted {
			return s.name(r)
		}
	}
	return ""
}

// card is what a card shows besides its state: the descriptor's words,
// the catalog's size and requirements, the settings' values, what it
// changes in Steam and the helper's status lines.
func (s *Service) card(ctx context.Context, id string, e catalog.Entry, inCat bool, d *descriptor.Descriptor, names map[uint32]string) ExtensionDoc {
	x := ExtensionDoc{ID: id, Name: id, Caveats: []string{}, Permissions: []string{}, Downloads: []DownloadDoc{},
		Settings: []SettingDoc{}, Actions: []ActionDoc{}, Status: []StatusLine{}, Requires: []string{},
		Core: e.Core, Size: e.Size}
	if inCat && len(e.Requires) > 0 {
		x.Requires = slices.Clone(e.Requires)
	}
	if d == nil {
		return x
	}
	x.Name, x.Summary, x.Category, x.Upstream = d.Name, d.Summary, d.Category, d.Upstream
	x.Core = x.Core || d.Core
	x.Copy = CopyDoc{Install: d.Copy.Install, Remove: d.Copy.Remove}
	if len(d.Caveats) > 0 {
		x.Caveats = slices.Clone(d.Caveats)
	}
	if !inCat && len(d.Requires) > 0 {
		x.Requires = slices.Clone(d.Requires)
	}
	if b := d.Build; b != nil {
		if x.Size == 0 {
			x.Size = b.Size
		}
		if len(b.Permissions) > 0 {
			x.Permissions = slices.Clone(b.Permissions)
		}
	}
	if d.Web != nil {
		x.Web = &WebDoc{Port: d.Web.Port, Label: d.Web.Label}
		x.WebRunning = s.serving(d.Web.Port) == id
	}
	x.RunsAsRoot = d.RunsAsRoot()
	x.ModuleOptions = d.HasModuleOptions()
	x.Steam = steamDoc(d, names)
	for _, dl := range d.Downloads {
		x.Downloads = append(x.Downloads, DownloadDoc{What: dl.What, From: dl.From, Checked: dl.Checked, RunsCode: dl.RunsCode, When: dl.When})
	}
	ext := s.ext(id, d)
	modules := moduleSettings(d)
	for _, st := range d.Settings {
		choices := []string{}
		if len(st.Choices) > 0 {
			choices = slices.Clone(st.Choices)
		}
		x.Settings = append(x.Settings, SettingDoc{Key: st.Key, Type: st.Type, Label: st.Label, Help: st.Help,
			Restart: st.Restart, Choices: choices, Value: ext.Settings[st.Key], NeedsPassword: modules[st.Key],
			Required: st.Required})
	}
	for _, a := range d.Actions {
		ad := ActionDoc{Name: a.Name, Label: a.Label}
		if c := a.Confirm; c != nil {
			ad.Confirm = &ConfirmDoc{Title: c.Title, Body: c.Body, Button: c.Button, Tone: c.Tone}
		}
		x.Actions = append(x.Actions, ad)
	}
	hctx, cancel := context.WithTimeout(ctx, helperStatusWait)
	lines := HelperFor(id).Status(hctx, ext)
	cancel()
	for _, l := range lines {
		if l.Text != "" {
			x.Status = append(x.Status, l)
		}
	}
	return x
}

// cardFacts is what a card's state follows from besides the last pass's
// view.
type cardFacts struct {
	inWant    bool         // wanted or core, or required by one
	note      string       // why its helper did not finish
	lines     []StatusLine // its helper's status lines
	settingUp bool         // its helper's Install runs, waits for its turn or comes again by itself
	blocker   string       // a requirement whose image cannot be had, by name
}

// cardState is a card's state and why: the first that applies of
// installing (its image downloads or seals, or its helper sets it up),
// needs-attention, restart-needed, installed, not-in-this-version and
// not-installed. A wanted extension that is none of these is installing
// too: its image is still to come or the pass that proposes it still to
// run. One that cannot get there by itself needs attention instead
// (attention), so installing never lasts.
func (s *Service) cardState(x ExtensionStatus, v *view, f cardFacts) (string, string) {
	if x.State == StateDownloading || f.settingUp {
		return StateInstalling, ""
	}
	if why := attention(x, v, f); why != "" {
		return StateNeedsAttention, why
	}
	switch {
	case x.State == StateRestartNeeded && (x.Mounted || f.inWant):
		return StateRestartNeeded, ""
	case x.Mounted && !f.inWant:
		return StateRestartNeeded, "" // removed: its files stay until the restart
	case x.Mounted && v.restart && s.optionsChange(x.ID, v):
		return StateRestartNeeded, ""
	case x.Mounted:
		return StateInstalled, ""
	case x.State == StateNotInThisVersion:
		return StateNotInThisVersion, notInVersionText
	case f.inWant:
		return StateInstalling, ""
	}
	return StateNotInstalled, "" // also one removed before the restart that would have added it
}

// attention is why a card needs attention, or "": its image cannot be had,
// its set failed its trial or this boot could not start it, its helper did
// not finish, a status line of tone error while it runs, or, wanted and not
// running, nothing it waits for will bring it (a requirement whose image
// cannot be had, a start without extensions, tries that could not be
// written, no boot report).
func attention(x ExtensionStatus, v *view, f cardFacts) string {
	switch {
	case x.State == StateNeedsAttention && f.inWant: // the last pass's; one removed since falls through
		return attentionReason(x, v)
	case f.note != "" && (f.inWant || x.Mounted):
		return f.note
	case x.Mounted:
		if f.inWant {
			for _, l := range f.lines {
				if l.Tone == "error" {
					return l.Text
				}
			}
		}
		return ""
	case !f.inWant || x.State == StateNotInThisVersion || x.State == StateRestartNeeded:
		return ""
	case slices.ContainsFunc(v.plan.Missing, func(e catalog.Entry) bool { return e.ID == x.ID }):
		return "" // its image is still to come
	case f.blocker != "":
		return needsText(f.blocker)
	case !slices.Contains(v.plan.Want, x.ID):
		return "" // the next pass sees it
	case v.rep.HasReason(store.ReasonCmdline) || v.rep.HasReason(store.ReasonSkipOnce):
		return startedOffText
	case v.rep.HasReason(store.ReasonTriesWrite) || v.rep.HasReason(store.ReasonNoReport):
		return notTriedText
	}
	return skipText[x.Skipped]
}

func attentionReason(x ExtensionStatus, v *view) string {
	switch {
	case x.Error != "":
		return x.Error
	case v.plan.Blocked && slices.Contains(v.plan.IDs, x.ID):
		return blockedText
	case skipText[x.Skipped] != "":
		return skipText[x.Skipped]
	}
	return "It did not start. Restart to try again, or remove it."
}

// optionsChange reports whether the module options id's settings render
// now differ from those this boot started it with.
func (s *Service) optionsChange(id string, v *view) bool {
	d := s.desc(id)
	if d == nil || !d.HasModuleOptions() {
		return false
	}
	var booted []string
	if v.booted != nil {
		booted = ownOptions(d, v.booted.Options)
	}
	return !slices.Equal(normalized(booted), normalized(s.optionsOf(id)))
}

// requiredBy lists the extensions in want that need id, directly or not.
func requiredBy(cat *catalog.Catalog, want map[string]bool, id string) []string {
	out := []string{}
	for _, dep := range cat.Dependents(id) {
		if want[dep] {
			out = append(out, dep)
		}
	}
	return out
}

// restartReason says what a restart would finish, by name; with skip, that
// the next start is without extensions and it takes the restart after it.
func restartReason(skip bool, adding, removing, changing []string) string {
	var parts []string
	if len(adding) > 0 {
		parts = append(parts, "adding "+joinNames(adding))
	}
	if len(removing) > 0 {
		parts = append(parts, "removing "+joinNames(removing))
	}
	if len(changing) > 0 {
		parts = append(parts, "changing the settings of "+joinNames(changing))
	}
	what := "changing extensions"
	if len(parts) > 0 {
		what = joinNames(parts)
	}
	if skip {
		return skipOnceText + " Restart again after it to finish " + what + "."
	}
	return "Restart to finish " + what + "."
}

// skipOnceText is what a start without extensions does to a change that
// waits for a restart.
const skipOnceText = "The next start is without extensions."

// joinNames is "A", "A and B" or "A, B and C".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// desc returns id's shipped descriptor, nil when the image ships none or
// it cannot be read (descOf says which).
func (s *Service) desc(id string) *descriptor.Descriptor {
	d, _ := s.descOf(id)
	return d
}

// descOf returns id's shipped descriptor, read once per Service (the image
// never changes during a boot): nil without an error when the image ships
// none. A descriptor that could not be read is tried again next time.
func (s *Service) descOf(id string) (*descriptor.Descriptor, error) {
	if !manifest.ValidExtensionID(id) {
		return nil, nil // never a path outside the descriptors
	}
	s.cc.descMu.Lock()
	defer s.cc.descMu.Unlock()
	if d, ok := s.cc.descs[id]; ok {
		return d, nil
	}
	d, err := Shipped(id)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		d = nil
	case err != nil:
		shippedErr(id, err) // the document is built every 5 s
		return nil, err
	default:
		shippedErr(id, nil)
	}
	if s.cc.descs == nil {
		s.cc.descs = map[string]*descriptor.Descriptor{}
	}
	s.cc.descs[id] = d
	return d, nil
}

// unreadableText is why a change is refused when a descriptor it must
// check cannot be read.
const unreadableText = "VaporOS couldn't read the details it needs to check this change, so nothing changed. Restart VaporOS, then try again."

// needDescs fails closed: every id of ids the booted catalog lists must
// have a shipped descriptor that reads, or the change is refused (409),
// rather than taken as one that runs nothing as root and conflicts with
// nothing.
func (s *Service) needDescs(cat *catalog.Catalog, ids ...string) error {
	for _, id := range ids {
		if _, ok := cat.Get(id); !ok {
			continue
		}
		if d, err := s.descOf(id); err != nil || d == nil {
			if err == nil {
				log.Printf("extensions: %s: the image ships no descriptor for it", id)
			}
			return refuse(http.StatusConflict, unreadableText)
		}
	}
	return nil
}

// publishGap is the least time between two extensions.state events, and
// publishPoll how often the document is built anyway, for what changes
// without telling (a helper's status lines). Variables for tests.
var (
	publishGap  = 250 * time.Millisecond
	publishPoll = 5 * time.Second
)

// changed tells the publisher the document may have changed. It never
// blocks.
func (s *Service) changed() { s.signal(s.cc.dirty) }

// publishLoop sends the document as extensions.state whenever it changed,
// at most every publishGap.
func (s *Service) publishLoop(ctx context.Context) {
	var sent []byte
	var last time.Time
	poll := time.NewTicker(publishPoll)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.cc.dirty:
		case <-poll.C:
		}
		if wait := publishGap - time.Since(last); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		b, err := json.Marshal(s.Document(ctx))
		if err != nil || bytes.Equal(b, sent) {
			continue
		}
		sent, last = b, time.Now()
		s.cc.publish("extensions.state", json.RawMessage(b))
	}
}
