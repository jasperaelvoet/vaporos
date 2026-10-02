package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func randomBytes(seed uint64, n int) []byte {
	b := make([]byte, n)
	x := seed*0x9E3779B97F4A7C15 + 1
	for i := range b {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b[i] = byte(x)
	}
	return b
}

func TestIndexFormat(t *testing.T) {
	data := randomBytes(1, 3*BlockSize+100) // the last block is short
	var buf bytes.Buffer
	if err := WriteIndex(bytes.NewReader(data), int64(len(data)), &buf); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	// As docs/CONTRACTS.md "Block index" spells it out, byte by byte.
	want := []byte("VOSBIDX1")
	want = binary.LittleEndian.AppendUint32(want, 4096)
	want = binary.LittleEndian.AppendUint32(want, 16)
	want = binary.LittleEndian.AppendUint64(want, uint64(len(data)))
	want = append(want, make([]byte, 8)...)
	for off := 0; off < len(data); off += 4096 {
		s := sha256.Sum256(data[off:min(off+4096, len(data))])
		want = append(want, s[:16]...)
	}
	if !bytes.Equal(b, want) {
		t.Fatalf("index is\n%x\nwant\n%x", b, want)
	}

	x, err := parseIndex(b, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(x.hashes) != 4 || x.blockLen(3) != 100 || x.blockLen(0) != BlockSize {
		t.Fatalf("parsed %d hashes, last block %d bytes", len(x.hashes), x.blockLen(3))
	}
	if x.hashes[3] != hashBlock(data[3*BlockSize:]) {
		t.Fatal("last hash")
	}

	bad := map[string]func([]byte) []byte{
		"magic":        func(b []byte) []byte { b[0] = 'X'; return b },
		"block size":   func(b []byte) []byte { binary.LittleEndian.PutUint32(b[8:], 8192); return b },
		"hash size":    func(b []byte) []byte { binary.LittleEndian.PutUint32(b[12:], 32); return b },
		"image size":   func(b []byte) []byte { binary.LittleEndian.PutUint64(b[16:], 5); return b },
		"short":        func(b []byte) []byte { return b[:len(b)-1] },
		"long":         func(b []byte) []byte { return append(b, 0) },
		"header only":  func(b []byte) []byte { return b[:indexHeaderSize] },
		"not an index": func([]byte) []byte { return []byte("{}") },
	}
	for name, mutate := range bad {
		if _, err := parseIndex(mutate(bytes.Clone(b)), int64(len(data))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := parseIndex(b, int64(len(data))+1); err == nil {
		t.Error("accepted an index of another size")
	}
}

func TestIndexFileAndCLI(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	img := filepath.Join(dir, "root.erofs")
	data := randomBytes(2, 10*BlockSize)
	if err := os.WriteFile(img, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	stdout = &out
	defer func() { stdout = os.Stdout }()
	if rc := CLI("index", []string{img}); rc != 0 {
		t.Fatalf("vos index: exit %d", rc)
	}
	if !strings.Contains(out.String(), img+".idx") {
		t.Fatalf("output %q", out.String())
	}
	b, err := os.ReadFile(img + ".idx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseIndex(b, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(img + ".idx"); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".idx-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	if rc := CLI("index", nil); rc != 2 {
		t.Fatalf("no argument: exit %d", rc)
	}
	if rc := CLI("index", []string{dir}); rc != 1 {
		t.Fatalf("a directory: exit %d", rc)
	}
}

// failingReaderAt fails at and after byte at.
type failingReaderAt struct {
	r  io.ReaderAt
	at int64
}

func (f failingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) <= f.at {
		return f.r.ReadAt(p, off)
	}
	n := 0
	if off < f.at {
		n, _ = f.r.ReadAt(p[:f.at-off], off)
	}
	return n, errors.New("I/O error")
}

func TestScanBlocks(t *testing.T) {
	data := randomBytes(3, ChunkSize+5*BlockSize)
	var read int64
	hashes, err := scanBlocks(context.Background(), bytes.NewReader(data), int64(len(data)), func(n int64) { read += n })
	if err != nil || len(hashes) != numBlocks(int64(len(data))) || read != int64(len(data)) {
		t.Fatalf("%d hashes, %d bytes read, err %v", len(hashes), read, err)
	}
	if hashes[len(hashes)-1] != hashBlock(data[len(data)-BlockSize:]) {
		t.Fatal("last hash")
	}
	// A read error keeps the blocks of the chunks before it.
	hashes, err = scanBlocks(context.Background(), failingReaderAt{bytes.NewReader(data), ChunkSize + 100}, int64(len(data)), nil)
	if err == nil || len(hashes) != ChunkSize/BlockSize {
		t.Fatalf("%d hashes, err %v", len(hashes), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanBlocks(ctx, bytes.NewReader(data), int64(len(data)), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
