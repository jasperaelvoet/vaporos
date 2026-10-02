package extensions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type published struct {
	topic string
	data  map[string]string
}

func capture(s *Service) *[]published {
	var out []published
	s.publish = func(topic string, data any) {
		out = append(out, published{topic, data.(map[string]string)})
	}
	return &out
}

func TestPollMessages(t *testing.T) {
	e := newEnv(t)
	s, _ := e.service()
	got := capture(s)
	dir := filepath.Join(gamerRuntimeDir, messagesRel)

	s.pollMessages() // no directory yet
	for _, text := range []string{
		"TruckersMP did not start: the TruckersMP files are out of date. Update them on the TruckersMP card.",
		"Star Citizen did not start because its extension is not active right now (star-citizen/launcher). Check Extensions in VaporOS.",
	} {
		must(t, writeMessage(text))
		time.Sleep(time.Microsecond) // distinct names
	}
	// What else a game could leave there.
	writeFile(t, filepath.Join(dir, "notes.txt"), "not a message")
	writeFile(t, filepath.Join(dir, "100.json"), "{not json")
	writeFile(t, filepath.Join(dir, "101.json"), `{"level":"error","text":"line one\nline two\u0007   end"}`)
	writeFile(t, filepath.Join(dir, "102.json"), `{"level":"warning","text":"`+strings.Repeat("é", 400)+`"}`)
	secret := filepath.Join(t.TempDir(), "secret.json")
	writeFile(t, secret, `{"level":"warning","text":"read through a symlink"}`)
	must(t, os.Symlink(secret, filepath.Join(dir, "103.json")))

	s.pollMessages()
	var texts []string
	for _, p := range *got {
		if p.topic != "system.message" || p.data["level"] != "warning" {
			t.Errorf("published %+v", p)
		}
		texts = append(texts, p.data["text"])
	}
	if len(texts) != 4 || texts[0] != "line one line two end" || utf8.RuneCountInString(texts[1]) != maxMessageText ||
		!strings.HasSuffix(texts[1], "…") || !strings.HasPrefix(texts[2], "TruckersMP did not start") || !strings.HasPrefix(texts[3], "Star Citizen") {
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
		writeFile(t, filepath.Join(dir, "17593980000000000"+strings.Repeat("0", 2-len(itoa(uint64(i))))+itoa(uint64(i))+".json"),
			`{"level":"warning","text":"refused `+itoa(uint64(i))+`"}`)
	}
	s.pollMessages()
	if len(*got) != maxMessages || (*got)[0].data["text"] != "refused 15" || (*got)[4].data["text"] != "refused 19" {
		t.Fatalf("published %+v", *got)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("left behind: %v", ents)
	}
}

func TestPollMessagesRefusesASymlinkedDirectory(t *testing.T) {
	e := newEnv(t)
	s, _ := e.service()
	got := capture(s)
	elsewhere := t.TempDir()
	writeFile(t, filepath.Join(elsewhere, "1.json"), `{"level":"warning","text":"x"}`)
	must(t, os.MkdirAll(filepath.Join(gamerRuntimeDir, "vos"), 0o700))
	must(t, os.Symlink(elsewhere, filepath.Join(gamerRuntimeDir, messagesRel)))
	s.pollMessages()
	if len(*got) != 0 {
		t.Errorf("published %+v", *got)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "1.json")); err != nil {
		t.Errorf("deleted through a symlink: %v", err)
	}
}
