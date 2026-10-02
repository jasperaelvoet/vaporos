package extensions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

type published struct {
	topic string
	data  map[string]any
}

func capture(s *Service) *[]published {
	var out []published
	s.publish = func(topic string, data any) {
		out = append(out, published{topic, data.(map[string]any)})
	}
	return &out
}

func TestPollMessages(t *testing.T) {
	e := newEnv(t)
	s, _ := e.service()
	got := capture(s)
	dir := filepath.Join(config.GamerRuntimeDir, messagesRel)
	writeFile(t, filepath.Join(config.ExtDescriptorsDir, "truckersmp.json"), truckersmpDesc)
	writeFile(t, filepath.Join(config.ExtDescriptorsDir, "star-citizen.json"), starCitizenDesc)

	s.pollMessages() // no directory yet
	for _, r := range []launchRecord{
		{Code: codeHookFailed, ID: "truckersmp", Detail: "the TruckersMP files are out of date"},
		{Code: codeNotMounted, ID: "star-citizen", Detail: "shortcut star-citizen/launcher: this boot did not mount it"},
		{Code: codeHookFailed, ID: "coolercontrol", Detail: "no descriptor"},
	} {
		must(t, writeMessage(r))
		time.Sleep(time.Microsecond) // distinct names
	}
	// What else a game could leave there.
	writeFile(t, filepath.Join(dir, "notes.txt"), "not a message")
	writeFile(t, filepath.Join(dir, "100.json"), "{not json")
	writeFile(t, filepath.Join(dir, "101.json"), `{"level":"warning","text":"Your PC is infected. Call 555-0100"}`)
	writeFile(t, filepath.Join(dir, "102.json"), `{"code":"pwned","id":"truckersmp","detail":"x"}`)
	writeFile(t, filepath.Join(dir, "103.json"), `{"code":"hook-failed","id":"Truckers MP","detail":"x"}`)
	secret := filepath.Join(t.TempDir(), "secret.json")
	writeFile(t, secret, `{"code":"hook-failed","id":"truckersmp","detail":"read through a symlink"}`)
	must(t, os.Symlink(secret, filepath.Join(dir, "104.json")))

	s.pollMessages()
	var texts []string
	for _, p := range *got {
		if p.topic != "system.message" || p.data["level"] != "warning" || p.data["welcome"] != false {
			t.Errorf("published %+v", p)
		}
		texts = append(texts, p.data["text"].(string))
	}
	want := []string{
		"TruckersMP couldn't start the game. Try again, or check its card in Extensions.",
		"Star Citizen didn't start because its extension isn't active. Open Extensions in VaporOS to check it.",
		"An extension couldn't start the game. Try again, or check Extensions in VaporOS.",
	}
	if strings.Join(texts, "\n") != strings.Join(want, "\n") {
		t.Fatalf("texts %q", texts)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "notes.txt" {
		t.Errorf("left behind: %v", ents)
	}
	if _, err := os.Stat(secret); err != nil {
		t.Errorf("the symlink's target went: %v", err)
	}

	// A flood: the newest few are shown, the rest dropped.
	*got = nil
	for i := range 20 {
		id := "truckersmp"
		if i >= 15 {
			id = "star-citizen"
		}
		writeFile(t, filepath.Join(dir, "17593980000000000"+strings.Repeat("0", 2-len(itoa(uint64(i))))+itoa(uint64(i))+".json"),
			`{"code":"hook-failed","id":"`+id+`","detail":"refused `+itoa(uint64(i))+`"}`)
	}
	s.pollMessages()
	if len(*got) != maxMessages || !strings.HasPrefix((*got)[0].data["text"].(string), "Star Citizen") ||
		!strings.HasPrefix((*got)[4].data["text"].(string), "Star Citizen") {
		t.Fatalf("published %+v", *got)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("left behind: %v", ents)
	}
}

// The detail is for the journal: on one line and bounded, never shown.
func TestParseLaunchRecord(t *testing.T) {
	r, ok := parseLaunchRecord([]byte(`{"code":"hook-failed","id":"truckersmp","detail":"line one\nline two\u0007   ` + strings.Repeat("é", 400) + `"}`))
	if !ok || !strings.HasPrefix(r.Detail, "line one line two ") || len([]rune(r.Detail)) != maxMessageDetail || !strings.HasSuffix(r.Detail, "…") {
		t.Fatalf("%+v %v", r, ok)
	}
	for _, bad := range []string{`{}`, `{"code":"not-mounted"}`, `{"code":"not-mounted","id":"../x"}`, `{"code":"","id":"truckersmp"}`, `[]`} {
		if _, ok := parseLaunchRecord([]byte(bad)); ok {
			t.Errorf("%s: accepted", bad)
		}
	}
}

// wordsHelper words one code of its own.
type wordsHelper struct{ NopHelper }

func (wordsHelper) MessageText(code string) (string, bool) {
	if code == "starting" {
		return "TruckersMP is already starting. Wait for the game to open.", true
	}
	return "", false
}

// A helper's command leaves a code its helper words; vosd shows the
// helper's sentence, never the file's, and drops codes it has none for.
func TestHelperMessages(t *testing.T) {
	e := newEnv(t)
	s, _ := e.service()
	got := capture(s)
	withHelper(t, "truckersmp", wordsHelper{})
	dir := filepath.Join(config.GamerRuntimeDir, messagesRel)
	must(t, WriteMessage("truckersmp", "starting", "a handoff unit is loaded"))
	time.Sleep(time.Microsecond)
	must(t, WriteMessage("truckersmp", "pwned", ""))
	time.Sleep(time.Microsecond)
	must(t, WriteMessage("star-citizen", "starting", "no words"))
	writeFile(t, filepath.Join(dir, "105.json"), `{"code":"starting","id":"truckersmp","text":"Call 555-0100"}`)
	for _, bad := range [][2]string{{"truckersmp", codeHookFailed}, {"truckersmp", "Bad Code"}, {"../x", "starting"}} {
		if WriteMessage(bad[0], bad[1], "") == nil {
			t.Errorf("WriteMessage(%q, %q) wrote", bad[0], bad[1])
		}
	}
	s.pollMessages()
	if len(*got) != 2 {
		t.Fatalf("published %+v", *got)
	}
	for _, p := range *got {
		if p.data["text"] != "TruckersMP is already starting. Wait for the game to open." || p.data["welcome"] != false {
			t.Errorf("published %+v", p)
		}
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("left behind: %v", ents)
	}
}

func TestPollMessagesRefusesASymlinkedDirectory(t *testing.T) {
	e := newEnv(t)
	s, _ := e.service()
	got := capture(s)
	elsewhere := t.TempDir()
	writeFile(t, filepath.Join(elsewhere, "1.json"), `{"code":"hook-failed","id":"truckersmp","detail":"x"}`)
	must(t, os.MkdirAll(filepath.Join(config.GamerRuntimeDir, "vos"), 0o700))
	must(t, os.Symlink(elsewhere, filepath.Join(config.GamerRuntimeDir, messagesRel)))
	s.pollMessages()
	if len(*got) != 0 {
		t.Errorf("published %+v", *got)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "1.json")); err != nil {
		t.Errorf("deleted through a symlink: %v", err)
	}
}

// A failed Install or Remove shows its helper's sentence for a Refusal,
// through any wrapping, and the fallback for anything else.
func TestRefusalNote(t *testing.T) {
	withHelper(t, "truckersmp", wordsHelper{})
	const fallback = "Setting up TruckersMP didn't finish. Try again, or remove it."
	for _, tc := range []struct {
		id   string
		err  error
		want string
	}{
		{"truckersmp", errors.Join(errors.New("x"), fmt.Errorf("install: %w", Refuse("starting", nil))), "TruckersMP is already starting. Wait for the game to open."},
		{"truckersmp", Refuse("pwned", errors.New("x")), fallback},
		{"truckersmp", errors.New("plain"), fallback},
		{"star-citizen", Refuse("starting", nil), fallback},
	} {
		if got := refusalNote(tc.id, tc.err, fallback); got != tc.want {
			t.Errorf("%s %v: %q", tc.id, tc.err, got)
		}
	}
}
