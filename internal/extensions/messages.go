package extensions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// Messages from `vos ext launch`, and from a helper's own commands, to the
// person at the control center. The dispatcher runs as the gaming user
// inside Steam, where nobody sees its output: when it refuses a launch it
// leaves a record in the user's runtime directory,
// $XDG_RUNTIME_DIR/vos/ext-messages/<unix nanoseconds>.json, saying what
// happened and to which extension. vosd words it from its own sentences
// (a helper's code from its helper's, MessageWords) and the shipped
// descriptor's name, publishes it as a system.message and deletes the
// file: whatever a game writes there, the person only ever reads
// VaporOS's words.

// messagesRel is the message directory in the user's runtime directory.
const messagesRel = "vos/ext-messages"

// Bounds on what vosd takes from the directory, which the gaming user and
// every game can fill: at most maxMessages per poll (older ones beyond
// that are dropped), each file at most maxMessageFile bytes, and a detail
// of at most maxMessageDetail characters on one line in the journal.
const (
	maxMessages      = 5
	maxMessageNames  = 256
	maxMessageFile   = 4 << 10
	maxMessageDetail = 300
)

var messageName = regexp.MustCompile(`^[0-9]{1,20}\.json$`)

// What a refusal was.
const (
	codeNotMounted = "not-mounted" // a shortcut whose extension this boot did not mount
	codeHookFailed = "hook-failed" // an extension's launch hook failed
)

// launchRecord is one refusal: its code, the extension, and the detail
// (an error's text) that only the journal gets.
type launchRecord struct {
	Code   string `json:"code"`
	ID     string `json:"id"`
	Detail string `json:"detail"`
}

// MessageWords is a helper whose own commands run as the gaming user
// where nobody sees their output (TruckersMP's multiplayer start, from
// Steam or Moonlight) and leave vosd coded records through WriteMessage.
// vosd words each record with MessageText, never with the file's text.
type MessageWords interface {
	// MessageText is the sentence for code, false for a code it has none
	// for.
	MessageText(code string) (string, bool)
}

// helperCodeRe is a helper's message code.
var helperCodeRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// WriteMessage leaves vosd a record that a command of extension id's
// helper refused with code, one its helper's MessageText words; detail
// goes to the journal only. It runs as the gaming user.
func WriteMessage(id, code, detail string) error {
	if !manifest.ValidExtensionID(id) || !helperCodeRe.MatchString(code) || code == codeNotMounted || code == codeHookFailed {
		return fmt.Errorf("message %q of %q is not a helper's", code, id)
	}
	return writeMessage(launchRecord{Code: code, ID: id, Detail: detail})
}

// writeMessage leaves r for vosd. It runs as the gaming user.
func writeMessage(r launchRecord) error {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(rt) {
		rt = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	dir := filepath.Join(rt, messagesRel)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// vosd logs no more of the detail and reads no more than
	// maxMessageFile: 300 characters, each at most 6 bytes as JSON, keep
	// any record well inside that, so a long error is still told.
	r.Detail = oneLine(r.Detail, maxMessageDetail)
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, strconv.FormatInt(time.Now().UnixNano(), 10)+".json"))
}

// pollMessages publishes the newest refusals as system.message warnings
// the welcome screen leaves out (the person is at Steam, not the TV's
// status line), logs their details and deletes every message file.
func (s *Service) pollMessages() {
	names, err := gamerfs.ReadDirNames(config.GamerRuntimeDir, messagesRel, maxMessageNames)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("extensions: launch messages: %v", err)
		}
		return
	}
	names = slices.DeleteFunc(names, func(n string) bool { return !messageName.MatchString(n) })
	// Oldest first: by length, then by digits.
	slices.SortFunc(names, func(a, b string) int {
		if len(a) != len(b) {
			return len(a) - len(b)
		}
		return strings.Compare(a, b)
	})
	drop := max(len(names)-maxMessages, 0)
	if drop > 0 {
		log.Printf("extensions: dropping %d launch messages", drop)
	}
	for i, name := range names {
		rel := messagesRel + "/" + name
		if i >= drop {
			if b, err := gamerfs.ReadFile(config.GamerRuntimeDir, rel, maxMessageFile); err == nil {
				if r, ok := parseLaunchRecord(b); ok {
					log.Printf("extensions: %s: a Steam launch was refused (%s): %s", r.ID, r.Code, r.Detail)
					s.publish("system.message", map[string]any{"level": "warning", "text": r.text(), "welcome": false})
				}
			}
		}
		if err := gamerfs.Remove(config.GamerRuntimeDir, rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("extensions: launch message: %v", err)
		}
	}
}

// parseLaunchRecord reads a message file: a known code and a valid id,
// with the detail on one line and bounded.
func parseLaunchRecord(b []byte) (launchRecord, bool) {
	var r launchRecord
	if json.Unmarshal(b, &r) != nil || !manifest.ValidExtensionID(r.ID) {
		return launchRecord{}, false
	}
	if r.Code != codeNotMounted && r.Code != codeHookFailed {
		if _, ok := helperText(r); !ok {
			return launchRecord{}, false
		}
	}
	r.Detail = oneLine(r.Detail, maxMessageDetail)
	return r, true
}

// helperText is the sentence r's extension's helper has for its code.
func helperText(r launchRecord) (string, bool) {
	w, ok := HelperFor(r.ID).(MessageWords)
	if !ok || !helperCodeRe.MatchString(r.Code) {
		return "", false
	}
	text, ok := w.MessageText(r.Code)
	return text, ok && text != ""
}

// text is what the person reads: VaporOS's sentence, with the extension's
// name from its shipped descriptor (root's, in the image), or the one its
// helper has for its own code.
func (r launchRecord) text() string {
	if r.Code != codeNotMounted && r.Code != codeHookFailed {
		if text, ok := helperText(r); ok {
			return text
		}
	}
	name := ""
	if d, err := Shipped(r.ID); err == nil {
		name = d.Name
	}
	switch {
	case r.Code == codeNotMounted && name != "":
		return name + " didn't start because its extension isn't active. Open Extensions in VaporOS to check it."
	case r.Code == codeNotMounted:
		return "A game didn't start because its extension isn't active. Open Extensions in VaporOS to check it."
	case name != "":
		return name + " couldn't start the game. Try again, or check its card in Extensions."
	}
	return "An extension couldn't start the game. Try again, or check Extensions in VaporOS."
}

// oneLine joins text's words with single spaces, dropping control
// characters and invalid bytes, and cuts it to n characters.
func oneLine(text string, n int) string {
	text = strings.Join(strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == utf8.RuneError
	}), " ")
	if r := []rune(text); len(r) > n {
		text = string(r[:n-1]) + "…"
	}
	return text
}
