package fsverity

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// yes returns n bytes of `yes abcdefg`.
func yes(n int) io.Reader {
	return io.LimitReader(strings.NewReader(strings.Repeat("abcdefg\n", n/8+1)), int64(n))
}

// Vectors from `fsverity digest --hash-alg=sha256 --block-size=4096`
// (fsverity-utils), covering empty input, one block, a partial second block
// and Merkle trees of two and three levels.
func TestDigestMatchesFsverityUtils(t *testing.T) {
	for _, c := range []struct {
		name string
		in   io.Reader
		size int64
		want string
	}{
		{"empty", bytes.NewReader(nil), 0, "3d248ca542a24fc62d1c43b916eae5016878e2533c88238480b26128a1f1af95"},
		{"one byte", strings.NewReader("a"), 1, "bce75948b9e7510293f8f2720412af9697c1479281323f3f220623fb8e94b557"},
		{"one block", yes(4096), 4096, "0089e7662d1e807a948270f0503fd69e66b549e9df685e5fc4cca4e5308b594c"},
		{"block plus one", yes(4097), 4097, "a590eea2232d95d6603f3ec229eb650ead55860e31c05cb3e19251e8a30fe156"},
		{"two levels", yes(614400), 614400, "06bc015bceb5826cff0e09257111b4f6430f5f5eebcedd4f85123eaf7b4ef444"},
		{"three levels", yes(73400320), 73400320, "e77538c327318bddf8f93cdf066c0c7f9f134ec7126d2fcaa16e927a07844cc1"},
	} {
		got, size, err := Digest(c.in)
		if err != nil || got != c.want || size != c.size {
			t.Errorf("%s: %s %d %v, want %s %d", c.name, got, size, err, c.want, c.size)
		}
	}
}

func TestDigestFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x")
	if err := os.WriteFile(f, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := DigestFile(f); err != nil || got != "bce75948b9e7510293f8f2720412af9697c1479281323f3f220623fb8e94b557" {
		t.Fatal(got, err)
	}
	if _, err := DigestFile(f + ".missing"); err == nil {
		t.Fatal("no error for a missing file")
	}
}
