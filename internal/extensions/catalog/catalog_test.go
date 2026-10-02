package catalog

import (
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

const (
	a = "0000000000000000000000000000000000000000000000000000000000000001"
	b = "0000000000000000000000000000000000000000000000000000000000000002"
)

func sample() *Catalog {
	return &Catalog{Dispatcher: 1, Entries: []Entry{
		{ID: "proton", SHA256: a, Size: 10, FSVerity: b, Core: true},
		{ID: "coolercontrol", SHA256: b, Size: 20, FSVerity: a},
		{ID: "truckersmp", SHA256: a, Size: 30, FSVerity: a, Requires: []string{"proton"}},
	}}
}

func TestRoundTrip(t *testing.T) {
	c := sample()
	got, err := Parse(strings.NewReader(string(c.Format())))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) || !got.Equal(c) {
		t.Fatalf("round trip:\n%+v\n%+v", got, c)
	}
}

func TestFormatIsAshFriendly(t *testing.T) {
	// The initramfs reads it with `read -r kind id sha size fsv core req`.
	want := "ext truckersmp " + a + " 30 " + a + " - proton\n"
	if !strings.Contains(string(sample().Format()), want) {
		t.Fatalf("missing line %q in\n%s", want, sample().Format())
	}
}

func TestParseRejects(t *testing.T) {
	for name, text := range map[string]string{
		"short":          "ext proton " + a + " 10 " + b + "\n",
		"bad id":         "ext Proton " + a + " 10 " + b + " -\n",
		"bad size":       "ext proton " + a + " ten " + b + " -\n",
		"zero size":      "ext proton " + a + " 0 " + b + " -\n",
		"bad sha":        "ext proton abc 10 " + b + " -\n",
		"bad core":       "ext proton " + a + " 10 " + b + " yes\n",
		"twice":          "ext proton " + a + " 10 " + b + " -\next proton " + a + " 10 " + b + " -\n",
		"order":          "ext truckersmp " + a + " 10 " + b + " - proton\next proton " + a + " 10 " + b + " -\n",
		"self":           "ext proton " + a + " 10 " + b + " - proton\n",
		"bad dispatcher": "dispatcher x\n",
	} {
		if _, err := Parse(strings.NewReader(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseIgnoresUnknownAndComments(t *testing.T) {
	c, err := Parse(strings.NewReader("# hi\n\nfuture thing 1 2\ndispatcher 3\next proton " + a + " 10 " + b + " core\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Dispatcher != 3 || len(c.Entries) != 1 || !c.Entries[0].Core {
		t.Fatalf("%+v", c)
	}
}

func TestQueries(t *testing.T) {
	c := sample()
	if got := c.Core(); !reflect.DeepEqual(got, []string{"proton"}) {
		t.Errorf("Core = %v", got)
	}
	if got := c.Closure([]string{"truckersmp", "nope"}); !reflect.DeepEqual(got, []string{"proton", "truckersmp"}) {
		t.Errorf("Closure = %v", got)
	}
	if got := c.Dependents("proton"); !reflect.DeepEqual(got, []string{"truckersmp"}) {
		t.Errorf("Dependents = %v", got)
	}
	if _, ok := c.Get("nope"); ok {
		t.Error("Get(nope)")
	}
	var nilCat *Catalog
	if _, ok := nilCat.Get("x"); ok || nilCat.IDs() != nil || len(nilCat.Core()) != 0 {
		t.Error("nil catalog")
	}
}

func TestFromManifest(t *testing.T) {
	m := &manifest.Manifest{Extensions: map[string]manifest.Extension{
		"truckersmp":   {Name: "ext-truckersmp.raw", Size: 3, SHA256: a, FSVerity: b, Requires: []string{"proton"}},
		"star-citizen": {Name: "ext-star-citizen.raw", Size: 4, SHA256: a, FSVerity: b, Requires: []string{"proton"}},
		"proton":       {Name: "ext-proton.raw", Size: 1, SHA256: a, FSVerity: b, Core: true},
		"aaa":          {Name: "ext-aaa.raw", Size: 2, SHA256: a, FSVerity: b},
	}}
	c := FromManifest(m)
	if got := c.IDs(); !reflect.DeepEqual(got, []string{"aaa", "proton", "star-citizen", "truckersmp"}) {
		t.Fatalf("order %v", got)
	}
	if c.Dispatcher != Dispatcher {
		t.Fatal(c.Dispatcher)
	}
	if _, err := Parse(strings.NewReader(string(c.Format()))); err != nil {
		t.Fatalf("FromManifest output does not parse: %v", err)
	}
	if got := FromManifest(nil); len(got.Entries) != 0 {
		t.Fatal(got)
	}
}

func TestLoadMissing(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope"))
	if !errors.Is(err, fs.ErrNotExist) || c == nil || len(c.Entries) != 0 {
		t.Fatalf("%v %+v", err, c)
	}
}
