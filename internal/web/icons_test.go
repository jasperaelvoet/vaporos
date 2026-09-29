package web

import (
	"bytes"
	"regexp"
	"testing"
)

// The inline logo in the legacy page header and icon.svg are the same drawing.
func TestLogoMatchesIcon(t *testing.T) {
	d := regexp.MustCompile(`<path d="([^"]+)"`)
	layout, _ := content.ReadFile("templates/legacy/layout.html")
	icon, _ := content.ReadFile("static/icon.svg")
	logo := layout[bytes.Index(layout, []byte(`{{define "logo"}}`)):]
	a, b := d.FindAllSubmatch(logo, -1), d.FindAllSubmatch(icon, -1)
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("logo has %d paths, icon.svg %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i][1], b[i][1]) {
			t.Errorf("path %d differs: %s vs %s", i, a[i][1], b[i][1])
		}
	}
}
