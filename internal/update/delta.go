package update

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"

	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// Block downloads (docs/CONTRACTS.md "Block index"). Variables so tests
// can change them.
var (
	rangeGap     int64 = 64 << 10 // join runs of missing blocks across gaps this small
	maxRange     int64 = 8 << 20  // and cut them at this
	rangeWorkers       = 8        // range requests at a time
	// maxDownloadShare: when more of root than this is missing, streaming
	// it whole is as cheap and simpler.
	maxDownloadShare = 0.75
)

// fallbackError means the block index cannot finish this stage, and
// streaming the whole root can.
type fallbackError struct{ err error }

func (e *fallbackError) Error() string { return e.err.Error() }
func (e *fallbackError) Unwrap() error { return e.err }

// Where a block of the new image comes from, besides an offset in the
// booted slot.
const (
	inPlace  = -1 // the idle slot holds it already
	download = -2
)

// blockPlan says where each block of the new image comes from, and how
// many bytes come from where.
type blockPlan struct {
	from                  []int64
	kept, copied, toFetch int64
	requested             int64 // bytes the range requests ask for, gaps included
}

// writeRootBlocks is Write order 2 and 3 with the block index: it fills
// slot (the idle slot, open read-write) with m's root, keeping the blocks
// the slot already holds, copying the ones the booted slot (seedDev) holds
// and downloading the rest, then reads the slot back like writeRoot. It
// leaves slot open. A *fallbackError says to stream the whole root instead.
func writeRootBlocks(ctx context.Context, src *Source, m *manifest.Manifest, slot *os.File, dev, seedDev string, progress func(Progress)) error {
	root := m.Artifact(manifest.Root)
	prog := &blockProgress{rep: &reporter{fn: progress, version: m.Version}}
	prog.rep.reportAt("write", 0, 0, 0, true)

	idx, err := fetchIndex(ctx, src, m)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return &fallbackError{fmt.Errorf("block index: %w", err)}
	}

	var seed io.ReaderAt
	var seedLen int64
	if seedDev != "" && seedDev != dev {
		if f, err := os.Open(seedDev); err != nil {
			log.Printf("update: reading the running slot: %v", err)
		} else {
			defer f.Close()
			seed, seedLen = f, seedSize(f, root.Size)
		}
	}
	prog.scanTotal = root.Size + seedLen
	plan, err := planBlocks(ctx, idx, slot, seed, seedLen, prog)
	if err != nil {
		return err
	}
	if n := rangeBytes(missingRanges(plan, idx.size)); float64(n) > maxDownloadShare*float64(root.Size) {
		return &fallbackError{fmt.Errorf("%s of the %s image would be downloaded in blocks", humanBytes(n), humanBytes(root.Size))}
	}

	w := &slotWriter{f: slot}
	prog.copyTotal = plan.copied
	if err := copyBlocks(ctx, idx, plan, seed, w, prog); err != nil {
		return fmt.Errorf("writing %s: %w", dev, err)
	}
	ranges := missingRanges(plan, idx.size)
	plan.requested = rangeBytes(ranges)
	prog.fetchTotal = plan.requested
	if err := downloadBlocks(ctx, src, root.Name, idx, ranges, w, prog); err != nil {
		if errors.Is(err, errNoRanges) {
			return &fallbackError{err}
		}
		return err
	}
	prog.done()
	if err := slot.Sync(); err != nil {
		return fmt.Errorf("writing %s: %w", dev, err)
	}
	if err := verifySlot(ctx, dev, root, prog.rep); err != nil {
		if errors.Is(err, ErrChecksum) {
			return &fallbackError{err}
		}
		return err
	}
	log.Printf("update: %s from its block index: %s already in place, %s copied from the running slot, %s downloaded in %d ranges",
		m.Version, humanBytes(plan.kept), humanBytes(plan.copied), humanBytes(plan.requested), len(ranges))
	return nil
}

// fetchIndex downloads and parses m's block index.
func fetchIndex(ctx context.Context, src *Source, m *manifest.Manifest) (*blockIndex, error) {
	a := m.Artifact(manifest.Index)
	if a.Size > maxIndexSize {
		return nil, fmt.Errorf("%s is %d bytes, more than %d", a.Name, a.Size, maxIndexSize)
	}
	var buf bytes.Buffer
	buf.Grow(int(a.Size))
	if err := src.Fetch(ctx, a, &buf, nil); err != nil {
		return nil, err
	}
	x, err := parseIndex(buf.Bytes(), m.Artifact(manifest.Root).Size)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", a.Name, err)
	}
	return x, nil
}

// erofsMagic starts an erofs superblock, which sits at byte 1024.
const erofsMagic = 0xE0F5E1E2

// seedSize is how much of the booted slot to read: the erofs on it
// (blocks << blkszbits), or else as much as the new image; never more than
// the partition.
func seedSize(f *os.File, fallback int64) int64 {
	dev, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0
	}
	size := fallback
	var sb [64]byte
	if _, err := f.ReadAt(sb[:], 1024); err == nil && binary.LittleEndian.Uint32(sb[0:]) == erofsMagic {
		if bits := sb[12]; bits >= 9 && bits <= 16 {
			size = int64(binary.LittleEndian.Uint32(sb[36:])) << bits
		}
	}
	return min(size, dev)
}

// planBlocks reads both slots at once and decides where every block of
// the new image comes from. A slot that cannot be read to the end counts
// for what was read: those blocks are downloaded instead.
func planBlocks(ctx context.Context, idx *blockIndex, slot io.ReaderAt, seed io.ReaderAt, seedLen int64, prog *blockProgress) (*blockPlan, error) {
	var idle, booted []blockHash
	var idleErr, seedErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		idle, idleErr = scanBlocks(ctx, slot, idx.size, prog.scanned)
	}()
	if seed != nil && seedLen > 0 {
		booted, seedErr = scanBlocks(ctx, seed, seedLen, prog.scanned)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if idleErr != nil {
		log.Printf("update: reading the idle slot: %v", idleErr)
	}
	if seedErr != nil {
		log.Printf("update: reading the running slot: %v", seedErr)
	}

	at := make(map[blockHash]int64, len(booted))
	for i, h := range booted {
		if _, ok := at[h]; !ok {
			at[h] = int64(i) * BlockSize
		}
	}
	p := &blockPlan{from: make([]int64, len(idx.hashes))}
	for i, h := range idx.hashes {
		n := idx.blockLen(i)
		if i < len(idle) && idle[i] == h {
			p.from[i] = inPlace
			p.kept += n
		} else if off, ok := at[h]; ok && n == BlockSize {
			p.from[i] = off
			p.copied += n
		} else {
			p.from[i] = download
			p.toFetch += n
		}
	}
	return p, nil
}

// copyBlocks copies the blocks plan takes from the booted slot, in runs
// that are consecutive in both slots, and checks every one against the
// index: a block that does not match is downloaded instead.
func copyBlocks(ctx context.Context, idx *blockIndex, plan *blockPlan, seed io.ReaderAt, w *slotWriter, prog *blockProgress) error {
	buf := make([]byte, ChunkSize)
	maxRun := ChunkSize / BlockSize
	for i := 0; i < len(plan.from); {
		if plan.from[i] < 0 {
			i++
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		j := i + 1
		for j < len(plan.from) && j-i < maxRun && plan.from[j] == plan.from[j-1]+BlockSize {
			j++
		}
		run := buf[:(j-i)*BlockSize]
		k, _ := seed.ReadAt(run, plan.from[i])
		good := i // first block of the run of good blocks not yet written
		flush := func(end int) error {
			if end > good {
				lo, hi := (good-i)*BlockSize, (end-i)*BlockSize
				if err := w.WriteAt(run[lo:hi], int64(good)*BlockSize); err != nil {
					return err
				}
			}
			return nil
		}
		for b := i; b < j; b++ {
			lo := (b - i) * BlockSize
			if lo+BlockSize <= k && hashBlock(run[lo:lo+BlockSize]) == idx.hashes[b] {
				continue
			}
			if err := flush(b); err != nil {
				return err
			}
			good = b + 1
			plan.from[b] = download
			plan.copied -= BlockSize
			plan.toFetch += BlockSize
		}
		if err := flush(j); err != nil {
			return err
		}
		prog.copiedN(int64(j-i) * BlockSize)
		i = j
	}
	return nil
}

type byteRange struct{ off, end int64 }

// missingRanges joins the blocks to download into byte ranges of root:
// runs no more than rangeGap apart, cut at maxRange. The blocks in a gap
// come along again; they are checked and written like the others.
func missingRanges(plan *blockPlan, size int64) []byteRange {
	var rs []byteRange
	for i, from := range plan.from {
		if from != download {
			continue
		}
		off := int64(i) * BlockSize
		end := min(off+BlockSize, size)
		if n := len(rs); n > 0 && off-rs[n-1].end <= rangeGap && end-rs[n-1].off <= maxRange {
			rs[n-1].end = end
			continue
		}
		rs = append(rs, byteRange{off, end})
	}
	return rs
}

func rangeBytes(rs []byteRange) int64 {
	var n int64
	for _, r := range rs {
		n += r.end - r.off
	}
	return n
}

// downloadBlocks fetches ranges of root with rangeWorkers requests at a
// time, checks every block against the index and writes it. The first
// failure stops the others.
func downloadBlocks(ctx context.Context, src *Source, name string, idx *blockIndex, ranges []byteRange, w *slotWriter, prog *blockProgress) error {
	if len(ranges) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	jobs := make(chan byteRange)
	var wg sync.WaitGroup
	for range max(1, min(rangeWorkers, len(ranges))) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 1<<20)
			for r := range jobs {
				if err := fetchRange(ctx, src, name, idx, r, buf, w, prog); err != nil {
					cancel(err)
					return
				}
			}
		}()
	}
feed:
	for _, r := range ranges {
		select {
		case jobs <- r:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return context.Cause(ctx)
}

// fetchRange downloads one range of root, block-checked, into the slot.
func fetchRange(ctx context.Context, src *Source, name string, idx *blockIndex, r byteRange, buf []byte, w *slotWriter, prog *blockProgress) error {
	rd := src.OpenRange(ctx, name, r.off, r.end)
	defer rd.Close()
	for off := r.off; off < r.end; {
		n := min(int64(len(buf)), r.end-off)
		if _, err := io.ReadFull(rd, buf[:n]); err != nil {
			return fmt.Errorf("%s: bytes %d-%d: %w", name, off, off+n-1, err)
		}
		for b := int64(0); b < n; b += BlockSize {
			i := int((off + b) / BlockSize)
			if hashBlock(buf[b:min(b+BlockSize, n)]) != idx.hashes[i] {
				return &fallbackError{fmt.Errorf("%s: %w: block %d does not match the block index", name, ErrChecksum, i)}
			}
		}
		if err := w.WriteAt(buf[:n], off); err != nil {
			return fmt.Errorf("writing the idle slot: %w", err)
		}
		prog.fetchedN(n)
		off += n
	}
	return nil
}

// slotWriter writes into the idle slot from any goroutine and fsyncs every
// syncEvery bytes, so the page cache never holds much of the slot.
type slotWriter struct {
	f        *os.File
	mu       sync.Mutex
	unsynced int64
}

func (w *slotWriter) WriteAt(b []byte, off int64) error {
	if _, err := w.f.WriteAt(b, off); err != nil {
		return err
	}
	w.mu.Lock()
	w.unsynced += int64(len(b))
	sync := w.unsynced >= syncEvery
	if sync {
		w.unsynced = 0
	}
	w.mu.Unlock()
	if sync {
		return w.f.Sync()
	}
	return nil
}

// blockProgress reports writeRootBlocks as the write phase: reading the
// slots is its first 10%, copying the next 10%, and downloading the rest,
// with the download's bytes and total.
type blockProgress struct {
	mu                               sync.Mutex
	rep                              *reporter
	scanTotal, copyTotal, fetchTotal int64
	scanDone, copyDone, fetchDone    int64
}

func share(done, total int64, span int) int {
	if total <= 0 {
		return span
	}
	return int(min(done, total) * int64(span) / total)
}

func (p *blockProgress) scanned(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scanDone += n
	p.rep.reportAt("write", share(p.scanDone, p.scanTotal, 10), 0, 0, false)
}

func (p *blockProgress) copiedN(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.copyDone += n
	p.rep.reportAt("write", 10+share(p.copyDone, p.copyTotal, 10), 0, 0, false)
}

func (p *blockProgress) fetchedN(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fetchDone += n
	p.rep.reportAt("write", 20+share(p.fetchDone, p.fetchTotal, 80), p.fetchDone, p.fetchTotal, false)
}

func (p *blockProgress) done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rep.reportAt("write", 100, p.fetchDone, p.fetchTotal, true)
}
