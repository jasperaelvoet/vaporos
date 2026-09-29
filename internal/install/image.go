package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

// Progress phases update.WriteRoot and update.FetchBootFiles report.
const (
	phaseDownload = "download"
	phaseWrite    = "write"
	phaseVerify   = "verify"
)

// fetchBoot fetches the kernel and initramfs into a temporary directory,
// each checked against the manifest's size and sha256, before any disk is
// touched: a network problem or a damaged file then ends the install with
// the target disk as it was. boot.InstallEntry copies them from there.
func (in *installer) fetchBoot(ctx context.Context) error {
	in.report(StepProbe, 2, "Reading the kernel and initramfs from %s", in.src)
	work, err := os.MkdirTemp("", "vos-install-")
	if err != nil {
		return err
	}
	in.workDir = work
	err = update.FetchBootFiles(ctx, in.src.Source, in.man, work, func(p update.Progress) {
		if p.Phase == phaseDownload {
			in.reportBytes(StepProbe, 2, 3, p.Bytes, p.Total, "Reading the kernel and initramfs")
		}
	})
	if err != nil {
		return fmt.Errorf("the kernel and initramfs on %s: %w", in.src, err)
	}
	in.bootDir = work
	return nil
}

// writeRoot streams root.erofs from the source into slot a while hashing
// it, then reads the slot back from the disk and checks it again
// (update.WriteRoot, as `vos update` writes the idle slot). The image is
// never staged in RAM or on another disk.
func (in *installer) writeRoot(ctx context.Context) error {
	part := devName(in.parts.a)
	in.report(StepWrite, 10, "Writing VaporOS %s to %s", in.man.Version, part)
	err := update.WriteRoot(ctx, in.src.Source, in.man, devPath(in.parts.a), func(p update.Progress) {
		switch p.Phase {
		case phaseWrite:
			in.reportBytes(StepWrite, 10, 75, p.Bytes, p.Total, "Writing slot a")
		case phaseVerify:
			if in.step != StepVerify {
				in.report(StepVerify, 75, "Verifying %s", part)
			}
			in.reportBytes(StepVerify, 75, 90, p.Bytes, p.Total, "Verifying slot a")
		}
	})
	switch {
	case err == nil:
	case errors.Is(err, update.ErrChecksum) && in.step == StepVerify:
		return fmt.Errorf("slot a does not read back what was written; the disk may be failing: %w", err)
	case errors.Is(err, update.ErrChecksum):
		return fmt.Errorf("the image on %s is damaged: %w", in.src, err)
	default:
		return err
	}
	in.report(StepVerify, 90, "Image verified")
	return nil
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
