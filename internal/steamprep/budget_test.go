package steamprep

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// bigAccounts signs n accounts in, each with a localconfig.vdf of about
// size bytes: the fixture with a block for every app a big library
// would have run.
func (b *box) bigAccounts(n, size int) []uint32 {
	b.t.Helper()
	orig := b.steamFile("userdata/52079950/config/localconfig.vdf")
	var apps bytes.Buffer
	for i := 0; apps.Len() < size; i++ {
		id := strconv.Itoa(3000000 + i)
		apps.WriteString("\t\t\t\t\t\"" + id + "\"\n\t\t\t\t\t{\n\t\t\t\t\t\t\"LastPlayed\"\t\t\"1790000000\"\n\t\t\t\t\t\t\"Playtime\"\t\t\"12\"\n" +
			"\t\t\t\t\t\t\"cloud\"\n\t\t\t\t\t\t{\n\t\t\t\t\t\t\t\"last_sync_state\"\t\t\"synchronized\"\n\t\t\t\t\t\t}\n\t\t\t\t\t}\n")
	}
	anchor := []byte("\t\t\t\t\t\"7\"\n")
	big := bytes.Replace(orig, anchor, append(apps.Bytes(), anchor...), 1)

	var users strings.Builder
	users.WriteString("\"users\"\n{\n")
	var ids []uint32
	for i := range n {
		id := uint32(52079950 + i)
		ids = append(ids, id)
		fmt.Fprintf(&users, "\t\"%d\"\n\t{\n\t\t\"AccountName\"\t\t\"player%d\"\n\t}\n", uint64(0x0110000100000000)|uint64(id), i)
		b.write(steam.LocalConfigPath(b.root, id), big)
	}
	users.WriteString("}\n")
	b.write(filepath.Join(b.root, "config", "loginusers.vdf"), []byte(users.String()))
	return ids
}

// TestLargeLocalConfigsWithinBudget wraps launch options in four
// accounts' 60 MiB localconfig.vdf, far larger than real ones, well
// within the time gamescope waits for prepare.
func TestLargeLocalConfigsWithinBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("writes 240 MiB, and times the run")
	}
	b := newBox(t)
	ids := b.bigAccounts(4, 60<<20)
	b.desire(truckers(proton()))
	began := time.Now()
	b.runWith(Options{Budget: Budget})
	took := time.Since(began)
	t.Logf("4 accounts of 60 MiB: %s", took)
	if st := b.state(); st.Error != "" || st.Fingerprint == "" {
		t.Fatalf("error %q\n%s", st.Error, b.logs.String())
	}
	for _, id := range ids {
		if o, _ := b.launchOptions(id, ats); o != tokenATS+"%command%" {
			t.Errorf("%d: ATS %q", id, o)
		}
	}
	if took > Budget/2 {
		t.Errorf("took %s, more than half of the %s budget", took, Budget)
	}
}

// BenchmarkLargeLocalConfigs is one wrap or unwrap of four accounts'
// 60 MiB localconfig.vdf.
func BenchmarkLargeLocalConfigs(bm *testing.B) {
	b := newBox(bm)
	b.bigAccounts(4, 60<<20)
	d := truckers(proton())
	bm.ResetTimer()
	for i := range bm.N {
		d.Dispatcher = i%2 == 0
		bm.StopTimer()
		b.desire(d)
		bm.StartTimer()
		b.runWith(Options{Budget: time.Minute})
	}
}
