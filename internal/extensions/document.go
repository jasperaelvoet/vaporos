package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// StateInstalling is a card's state while its image downloads or seals, or
// waits for the pass that fetches it (GET /extensions).
const StateInstalling = "installing"

// Document is GET /extensions and the extensions.state event
// (docs/CONTRACTS.md "HTTP API").
type Document struct {
	Extensions []ExtensionDoc `json:"extensions"`
	Restart    RestartDoc     `json:"restart"`
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
	Status        []StatusLine        `json:"status"`
	Copy          CopyDoc             `json:"copy"`
	Requires      []string            `json:"requires"`
	RequiredBy    []string            `json:"required_by"`
	NeedsPassword bool                `json:"needs_password"`
}

type DownloadDoc struct {
	What     string `json:"what"`
	From     string `json:"from"`
	Checked  string `json:"checked"`
	RunsCode bool   `json:"runs_code"`
	When     string `json:"when"`
}

type SettingDoc struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	Help    string   `json:"help"`
	Restart bool     `json:"restart"`
	Choices []string `json:"choices"`
	Value   any      `json:"value"`
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
	installNoteText  = "Setting it up did not finish: "
	removeNoteText   = "Removing it did not finish: "
)

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
	notes := maps.Clone(s.cc.notes)
	s.mu.Unlock()
	wanted := v.wanted
	if w, err := store.Wanted(); err == nil {
		wanted = w
	}
	want := map[string]bool{}
	for _, id := range v.cat.Closure(append(slices.Clone(wanted), v.cat.Core()...)) {
		want[id] = true
	}
	doc := Document{Extensions: []ExtensionDoc{}}
	var adding, removing, changing []string
	for _, x := range st.Extensions {
		e, inCat := v.cat.Get(x.ID)
		d := s.desc(x.ID)
		xd := s.card(ctx, x.ID, e, inCat, d)
		xd.Wanted = x.Core || slices.Contains(wanted, x.ID)
		xd.Mounted, xd.Progress = x.Mounted, x.Progress
		xd.RequiredBy = requiredBy(v.cat, want, x.ID)
		inWant := want[x.ID] || xd.Wanted
		xd.State, xd.Reason = s.cardState(x, &v, inWant, notes[x.ID], xd.Status)
		if xd.State == StateRestartNeeded {
			switch {
			case !x.Mounted:
				adding = append(adding, xd.Name)
			case !inWant:
				removing = append(removing, xd.Name)
			default:
				changing = append(changing, xd.Name)
			}
		}
		doc.Extensions = append(doc.Extensions, xd)
	}
	doc.Restart.Needed = st.RestartNeeded
	if st.RestartNeeded {
		doc.Restart.Reason = restartReason(adding, removing, changing)
		doc.Restart.Auto = s.autoAllowed(&v)
	}
	return doc
}

// card is what a card shows besides its state: the descriptor's words,
// the catalog's size and requirements, the settings' values and the
// helper's status lines.
func (s *Service) card(ctx context.Context, id string, e catalog.Entry, inCat bool, d *descriptor.Descriptor) ExtensionDoc {
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
	}
	x.RunsAsRoot = d.RunsAsRoot()
	x.NeedsPassword = d.RunsAsRoot() || d.HasModuleOptions()
	for _, dl := range d.Downloads {
		x.Downloads = append(x.Downloads, DownloadDoc{What: dl.What, From: dl.From, Checked: dl.Checked, RunsCode: dl.RunsCode, When: dl.When})
	}
	ext := s.ext(id, d)
	for _, st := range d.Settings {
		choices := []string{}
		if len(st.Choices) > 0 {
			choices = slices.Clone(st.Choices)
		}
		x.Settings = append(x.Settings, SettingDoc{Key: st.Key, Type: st.Type, Label: st.Label, Help: st.Help,
			Restart: st.Restart, Choices: choices, Value: ext.Settings[st.Key]})
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

// cardState is a card's state and why, from what the last pass saw and
// the changes since: a download under way is installing; an image that
// cannot be had, a set that failed its trial or an extension its trial
// could not start, a helper that could not set it up or a status line of
// tone error needs attention; a change a restart applies is
// restart-needed; and what this boot runs is installed. A wanted
// extension the last pass did not see yet, or whose image is still to
// come, is installing.
func (s *Service) cardState(x ExtensionStatus, v *view, inWant bool, note string, lines []StatusLine) (string, string) {
	switch {
	case x.State == StateDownloading:
		return StateInstalling, ""
	case !inWant && !x.Mounted:
		return StateNotInstalled, "" // also one removed before the restart that would have added it
	case x.State == StateNotInThisVersion:
		return StateNotInThisVersion, notInVersionText
	case x.State == StateNeedsAttention:
		return StateNeedsAttention, attentionReason(x, v)
	case note != "" && (inWant || x.Mounted):
		return StateNeedsAttention, note
	}
	if inWant && x.Mounted {
		for _, l := range lines {
			if l.Tone == "error" {
				return StateNeedsAttention, l.Text
			}
		}
	}
	switch {
	case x.State == StateRestartNeeded:
		return StateRestartNeeded, ""
	case x.Mounted && !inWant:
		return StateRestartNeeded, "" // removed: its files stay until the restart
	case x.Mounted && v.restart && s.optionsChange(x.ID, v):
		return StateRestartNeeded, ""
	case x.Mounted:
		return StateInstalled, ""
	case !slices.Contains(v.plan.Want, x.ID) || slices.ContainsFunc(v.plan.Missing, func(e catalog.Entry) bool { return e.ID == x.ID }):
		return StateInstalling, ""
	case v.rep.HasReason(store.ReasonCmdline) || v.rep.HasReason(store.ReasonSkipOnce):
		return StateNeedsAttention, startedOffText
	case skipText[x.Skipped] != "":
		return StateNeedsAttention, skipText[x.Skipped]
	}
	return StateInstalling, "" // the next pass proposes it
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

// restartReason says what a restart would finish, by name.
func restartReason(adding, removing, changing []string) string {
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
	if len(parts) == 0 {
		return "Restart to finish changing extensions."
	}
	return "Restart to finish " + joinNames(parts) + "."
}

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

// desc returns id's shipped descriptor, read once per Service (the image
// never changes during a boot); nil when the image ships none.
func (s *Service) desc(id string) *descriptor.Descriptor {
	if !manifest.ValidExtensionID(id) {
		return nil // never a path outside the descriptors
	}
	s.cc.descMu.Lock()
	defer s.cc.descMu.Unlock()
	if d, ok := s.cc.descs[id]; ok {
		return d
	}
	d, err := Shipped(id)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("extensions: %s: %v", id, err)
		}
		d = nil
	}
	if s.cc.descs == nil {
		s.cc.descs = map[string]*descriptor.Descriptor{}
	}
	s.cc.descs[id] = d
	return d
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
