package update

import (
	"context"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// A boot report that cannot be read does not say the data partition lacks
// fs-verity, whatever its text: the images are counted and fetched.
func TestStageBootReportUnreadable(t *testing.T) {
	e := setup(t)
	img := e.makeImageExt(newVersion, 200, 100<<10, testExts...)
	e.write(config.ExtWantedPath(), "cooler\n")
	e.write(config.ExtBootPath(), `{"mode":"enabled","reason":"no-set no-verity"`)
	fs := e.fakeStore()
	images := img.m.Extensions["proton"].Size + img.m.Extensions["cooler"].Size

	res, err := Check(context.Background(), e.cfg(e.srcDir(img)), Options{})
	if want := int64(len(img.root())) + images; err != nil || res.Available == nil || res.Available.Size != want {
		t.Fatalf("check: %+v %v, want size %d", res, err, want)
	}
	var total int64
	opts := Options{Progress: func(p Progress) {
		if p.Phase == "download" {
			total = p.Total
		}
	}}
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), opts); err != nil {
		t.Fatal(err)
	}
	e.checkStaged(img)
	if strings.Join(fs.puts, " ") != "proton cooler" || total != bootFilesSize(img.m)+images {
		t.Fatalf("put %v, download total %d", fs.puts, total)
	}
}
