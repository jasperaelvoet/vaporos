package extensions

import (
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
