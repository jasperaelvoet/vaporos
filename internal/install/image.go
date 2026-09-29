package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

const (
	copyChunk = 4 << 20
	// syncEvery flushes the slot while writing, so progress follows the
	// disk rather than the page cache and the final fsync is short.
	syncEvery = 128 << 20
)

// openForWrite opens a partition for writing. On a real block device
// O_EXCL makes the kernel refuse if anything has it mounted or claimed.
func openForWrite(path string) (*os.File, error) {
	flags := os.O_WRONLY
	if fi, err := os.Stat(path); err == nil && fi.Mode()&os.ModeDevice != 0 {
		flags |= os.O_EXCL
	}
	return os.OpenFile(path, flags, 0)
}

// writeRoot streams root.erofs from the source into slot a, hashing as it
// goes; the image is never staged in RAM or on another disk.
func (in *installer) writeRoot(ctx context.Context) error {
	a := in.man.Artifacts["root"]
	in.report(StepWrite, 10, "Writing VaporOS %s to %s", in.man.Version, devName(in.parts.a))
	r, err := in.src.open(ctx, a.Name, a.Size)
	if err != nil {
		return fmt.Errorf("opening %s on %s: %w", a.Name, in.src, err)
	}
	defer r.Close()
	f, err := openForWrite(devPath(in.parts.a))
	if err != nil {
		return err
	}
	h := sha256.New()
	buf := make([]byte, copyChunk)
	var done, synced int64
	for done < a.Size {
		if err := ctx.Err(); err != nil {
			f.Close()
			return err
		}
		n := int(min(int64(len(buf)), a.Size-done))
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			f.Close()
			return fmt.Errorf("reading %s: %w", a.Name, err)
		}
		h.Write(buf[:n])
		if _, err := f.Write(buf[:n]); err != nil {
			f.Close()
			return fmt.Errorf("writing %s: %w", devName(in.parts.a), err)
		}
		done += int64(n)
		if done-synced >= syncEvery {
			if err := f.Sync(); err != nil {
				f.Close()
				return fmt.Errorf("writing %s: %w", devName(in.parts.a), err)
			}
			synced = done
		}
		in.reportBytes(StepWrite, 10, 75, done, a.Size, "Writing slot a")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", devName(in.parts.a), err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
		return fmt.Errorf("%s from %s is corrupt (sha256 %s, expected %s)", a.Name, in.src, got, a.SHA256)
	}
	return nil
}

// verify reads slot a back and checks the kernel and initramfs, so what
// boots is exactly what was signed.
func (in *installer) verify(ctx context.Context) error {
	a := in.man.Artifacts["root"]
	part := devPath(in.parts.a)
	in.report(StepVerify, 75, "Verifying %s", devName(in.parts.a))
	// Drop the page cache so the read-back comes from the disk, not from
	// the buffers that were just written.
	if _, err := in.env.run.Run(ctx, "blockdev", "--flushbufs", part); err != nil {
		in.env.logf("install: blockdev --flushbufs %s: %v", part, err)
	}
	f, err := os.Open(part)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	pr := &progressReader{r: io.LimitReader(f, a.Size), step: func(n int64) {
		in.reportBytes(StepVerify, 75, 88, n, a.Size, "Verifying slot a")
	}}
	n, err := copyCtx(ctx, h, pr)
	if err != nil {
		return fmt.Errorf("reading back %s: %w", devName(in.parts.a), err)
	}
	if n != a.Size {
		return fmt.Errorf("slot a is shorter than the image (%d of %d bytes)", n, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
		return fmt.Errorf("slot a does not read back what was written (sha256 %s, expected %s); the disk may be failing", got, a.SHA256)
	}

	in.report(StepVerify, 88, "Checking the kernel and initramfs")
	dir, err := in.stageBoot(ctx)
	if err != nil {
		return err
	}
	in.bootDir = dir
	in.report(StepVerify, 90, "Image verified")
	return nil
}

// stageBoot returns a directory holding a verified vmlinuz and
// initramfs.img for boot.InstallEntry: the source itself when it is local
// and uses those names, else a temporary copy.
func (in *installer) stageBoot(ctx context.Context) (string, error) {
	if d := in.src.dir(); d != "" && in.bootNamesCanonical() {
		for _, key := range []string{"kernel", "initrd"} {
			if err := verifyFile(filepath.Join(d, bootNames[key]), in.man.Artifacts[key]); err != nil {
				return "", err
			}
		}
		return d, nil
	}
	work, err := os.MkdirTemp("", "vos-install-")
	if err != nil {
		return "", err
	}
	in.workDir = work
	for _, key := range []string{"kernel", "initrd"} {
		if err := in.copyVerified(ctx, in.man.Artifacts[key], filepath.Join(work, bootNames[key])); err != nil {
			return "", err
		}
	}
	return work, nil
}

func (in *installer) bootNamesCanonical() bool {
	for key, name := range bootNames {
		if in.man.Artifacts[key].Name != name {
			return false
		}
	}
	return true
}

// copyVerified copies artifact a from the source to dst and checks it.
func (in *installer) copyVerified(ctx context.Context, a manifest.Artifact, dst string) error {
	r, err := in.src.open(ctx, a.Name, a.Size)
	if err != nil {
		return fmt.Errorf("opening %s on %s: %w", a.Name, in.src, err)
	}
	defer r.Close()
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := copyCtx(ctx, io.MultiWriter(f, h), io.LimitReader(r, a.Size))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("copying %s: %w", a.Name, err)
	}
	return checkDigest(a, n, h)
}

// verifyFile checks a local file against its manifest artifact.
func verifyFile(path string, a manifest.Artifact) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if err := checkDigest(a, n, h); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func checkDigest(a manifest.Artifact, n int64, h hash.Hash) error {
	if n != a.Size {
		return fmt.Errorf("%s is %d bytes, expected %d", a.Name, n, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
		return fmt.Errorf("%s is corrupt (sha256 %s, expected %s)", a.Name, got, a.SHA256)
	}
	return nil
}

// copyCtx is io.Copy that stops when ctx ends.
func copyCtx(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	return io.CopyBuffer(dst, ctxReader{ctx, src}, make([]byte, copyChunk))
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// progressReader calls step with the running byte count.
type progressReader struct {
	r    io.Reader
	n    int64
	step func(int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if n > 0 {
		p.step(p.n)
	}
	return n, err
}
