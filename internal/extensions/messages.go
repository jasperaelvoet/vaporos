package extensions

import (
	"encoding/json"
	"errors"
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
)

// Messages from `vos ext launch` to the person at the control center. The
// dispatcher runs as the gaming user inside Steam, where nobody sees its
// output: when it refuses a launch it leaves a file in the user's runtime
// directory, $XDG_RUNTIME_DIR/vos/ext-messages/<unix nanoseconds>.json
// ({"level":"warning","text":"…"}), and vosd publishes each as a
// system.message and deletes it.

// messagesRel is the message directory in the user's runtime directory.
const messagesRel = "vos/ext-messages"

// Bounds on what vosd takes from the directory, which the gaming user and
// every game can fill: at most maxMessages per poll (older ones beyond
// that are dropped), each file at most maxMessageFile bytes and its text
// at most maxMessageText characters on one line.
const (
	maxMessages     = 5
	maxMessageNames = 256
	maxMessageFile  = 4 << 10
	maxMessageText  = 300
)

var messageName = regexp.MustCompile(`^[0-9]{1,20}\.json$`)

type message struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// WriteMessage leaves text for vosd to show at the control center, as
// `vos ext launch` does: for a helper's own command that runs as the
// gaming user where nobody sees its output (TruckersMP's multiplayer
// start, from Steam or Moonlight).
func WriteMessage(text string) error { return writeMessage(text) }

// writeMessage leaves text for vosd. It runs as the gaming user.
func writeMessage(text string) error {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(rt) {
		rt = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	dir := filepath.Join(rt, messagesRel)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(message{Level: "warning", Text: text})
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

// pollMessages publishes the newest messages as system.message warnings
// and deletes every message file.
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
				if text := messageText(b); text != "" {
					s.publish("system.message", map[string]string{"level": "warning", "text": text})
				}
			}
		}
		if err := gamerfs.Remove(config.GamerRuntimeDir, rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("extensions: launch message: %v", err)
		}
	}
}

// messageText is a message file's text, on one line and bounded; "" when
// the file is not a message.
func messageText(b []byte) string {
	var m message
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	text := strings.Join(strings.FieldsFunc(m.Text, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == utf8.RuneError
	}), " ")
	if r := []rune(text); len(r) > maxMessageText {
		text = string(r[:maxMessageText-1]) + "…"
	}
	return text
}
