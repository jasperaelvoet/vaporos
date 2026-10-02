package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// The block index of a root image (docs/CONTRACTS.md "Block index"): a
// hash for every 4 KiB block, so a stage can take the blocks this machine
// already holds from its own slots and download only the rest.
const (
	indexMagic      = "VOSBIDX1"
	BlockSize       = 4096
	blockHashSize   = 16
	indexHeaderSize = 32
	// IndexSuffix makes the index's file name from the image's.
	IndexSuffix = ".idx"
)

// maxIndexSize bounds the index a stage downloads and keeps in memory: the
// index of a 16 GiB image, the largest slot.
const maxIndexSize = indexHeaderSize + (16<<30/BlockSize)*blockHashSize

type blockHash [blockHashSize]byte

func hashBlock(b []byte) blockHash {
	s := sha256.Sum256(b)
	return blockHash(s[:blockHashSize])
}

// numBlocks is how many blocks an image of size bytes has, the last one
// possibly short.
func numBlocks(size int64) int { return int((size + BlockSize - 1) / BlockSize) }

// blockIndex is a parsed index: the image size and a hash per block.
type blockIndex struct {
	size   int64
	hashes []blockHash
}

// blockLen is the length of block i.
func (x *blockIndex) blockLen(i int) int64 {
	return min(BlockSize, x.size-int64(i)*BlockSize)
}

// parseIndex reads an index and checks that it describes an image of size
// bytes.
func parseIndex(b []byte, size int64) (*blockIndex, error) {
	if len(b) < indexHeaderSize || string(b[:len(indexMagic)]) != indexMagic {
		return nil, errors.New("not a block index")
	}
	bs, hs := binary.LittleEndian.Uint32(b[8:]), binary.LittleEndian.Uint32(b[12:])
	if bs != BlockSize || hs != blockHashSize {
		return nil, fmt.Errorf("unsupported block index: %d-byte blocks, %d-byte hashes", bs, hs)
	}
	if n := binary.LittleEndian.Uint64(b[16:]); n != uint64(size) {
		return nil, fmt.Errorf("the block index is for a %d-byte image, not %d bytes", n, size)
	}
	n := numBlocks(size)
	if want := indexHeaderSize + int64(n)*blockHashSize; int64(len(b)) != want {
		return nil, fmt.Errorf("the block index has %d bytes, want %d", len(b), want)
	}
	x := &blockIndex{size: size, hashes: make([]blockHash, n)}
	for i := range x.hashes {
		off := indexHeaderSize + i*blockHashSize
		x.hashes[i] = blockHash(b[off : off+blockHashSize])
	}
	return x, nil
}

// WriteIndex writes the index of the size bytes r holds to w.
func WriteIndex(r io.Reader, size int64, w io.Writer) error {
	hdr := make([]byte, indexHeaderSize)
	copy(hdr, indexMagic)
	binary.LittleEndian.PutUint32(hdr[8:], BlockSize)
	binary.LittleEndian.PutUint32(hdr[12:], blockHashSize)
	binary.LittleEndian.PutUint64(hdr[16:], uint64(size))
	bw := bufio.NewWriter(w)
	if _, err := bw.Write(hdr); err != nil {
		return err
	}
	buf := make([]byte, ChunkSize)
	for done := int64(0); done < size; {
		n := min(int64(len(buf)), size-done)
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return fmt.Errorf("after %d of %d bytes: %w", done, size, err)
		}
		for b := int64(0); b < n; b += BlockSize {
			h := hashBlock(buf[b:min(b+BlockSize, n)])
			if _, err := bw.Write(h[:]); err != nil {
				return err
			}
		}
		done += n
	}
	return bw.Flush()
}

// IndexFile writes image's index to image+IndexSuffix (via a temp file, so
// a failed run leaves no partial index).
func IndexFile(image string) (string, error) {
	in, err := os.Open(image)
	if err != nil {
		return "", err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Size() == 0 {
		return "", fmt.Errorf("%s is not an image file", image)
	}
	out := image + IndexSuffix
	tmp, err := os.CreateTemp(filepath.Dir(out), ".idx-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	err = WriteIndex(in, fi.Size(), tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), out)
	}
	if err != nil {
		return "", fmt.Errorf("indexing %s: %w", image, err)
	}
	return out, nil
}

// scanBlocks hashes the blocks of the first size bytes of r, in ChunkSize
// reads. onRead runs after each read with the bytes read so far. A read
// error stops the scan: the hashes before it are returned with the error.
func scanBlocks(ctx context.Context, r io.ReaderAt, size int64, onRead func(n int64)) ([]blockHash, error) {
	hashes := make([]blockHash, 0, numBlocks(size))
	buf := make([]byte, ChunkSize)
	for done := int64(0); done < size; {
		if err := ctx.Err(); err != nil {
			return hashes, err
		}
		n := min(int64(len(buf)), size-done)
		k, err := r.ReadAt(buf[:n], done)
		if int64(k) < n {
			if err == nil || errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return hashes, err
		}
		for b := int64(0); b < n; b += BlockSize {
			hashes = append(hashes, hashBlock(buf[b:min(b+BlockSize, n)]))
		}
		done += n
		if onRead != nil {
			onRead(n)
		}
	}
	return hashes, nil
}
