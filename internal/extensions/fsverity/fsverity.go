// Package fsverity computes the fs-verity file digest of a file's contents:
// the value the kernel's FS_IOC_MEASURE_VERITY reports for a file sealed
// with SHA-256, 4096-byte Merkle tree blocks and no salt, and the value
// `fsverity digest --hash-alg=sha256 --block-size=4096` prints. VaporOS
// identifies extension images by it (docs/CONTRACTS.md "Extensions").
//
// The algorithm is the kernel's (Documentation/filesystems/fsverity.rst):
// the data is split into blocks, zero-padded, and hashed; the hashes are
// packed into blocks and hashed again, level by level, until one block is
// left, whose hash is the Merkle tree root. The file digest is the hash of
// the 256-byte fsverity_descriptor that holds that root and the data size.
package fsverity

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
)

const (
	BlockSize     = 4096
	logBlockSize  = 12
	hashAlgSHA256 = 1
	hashSize      = sha256.Size
)

// Digest returns the fs-verity digest of everything r yields, as 64 hex
// digits.
func Digest(r io.Reader) (string, int64, error) {
	root, size, err := merkleRoot(r)
	if err != nil {
		return "", 0, err
	}
	var desc [256]byte
	desc[0] = 1 // version
	desc[1] = hashAlgSHA256
	desc[2] = logBlockSize
	desc[3] = 0 // salt size
	binary.LittleEndian.PutUint64(desc[8:16], uint64(size))
	copy(desc[16:16+64], root[:])
	sum := sha256.Sum256(desc[:])
	return hex.EncodeToString(sum[:]), size, nil
}

// DigestFile returns the fs-verity digest of a file's contents.
func DigestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	d, _, err := Digest(f)
	return d, err
}

// merkleRoot hashes r's data blocks (level 0) and then each level of hashes
// until one block remains. An empty input has an all-zero root; input that
// fits in one block has that block's hash as its root.
func merkleRoot(r io.Reader) ([hashSize]byte, int64, error) {
	var zero [hashSize]byte
	level, size, err := hashBlocks(r)
	if err != nil {
		return zero, 0, err
	}
	if size == 0 {
		return zero, 0, nil
	}
	for len(level) > hashSize {
		next, _, err := hashBlocks(newBytesReader(level))
		if err != nil {
			return zero, 0, err
		}
		level = next
	}
	var root [hashSize]byte
	copy(root[:], level)
	return root, size, nil
}

// hashBlocks reads r in BlockSize blocks, zero-padding the last, and returns
// the concatenated SHA-256 of each block and the number of bytes read.
func hashBlocks(r io.Reader) ([]byte, int64, error) {
	var out []byte
	var size int64
	buf := make([]byte, BlockSize)
	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			clear(buf[n:])
			sum := sha256.Sum256(buf)
			out = append(out, sum[:]...)
			size += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return out, size, nil
		}
		if err != nil {
			return nil, 0, err
		}
	}
}

type bytesReader struct {
	b []byte
}

func newBytesReader(b []byte) *bytesReader { return &bytesReader{b: b} }

func (r *bytesReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}
