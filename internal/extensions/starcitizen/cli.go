package starcitizen

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/extensions"
)

const usage = "usage: vos ext star-citizen fetch-installer --prefix DIR"

// fetchResult is fetch-installer's one line on stdout: the installer it
// fetched, or the code it refused with.
type fetchResult struct {
	Installer string `json:"installer,omitempty"`
	Version   string `json:"version,omitempty"`
	Refused   string `json:"refused,omitempty"`
}

// cli is `vos ext star-citizen <verb>`.
func cli(args []string) int {
	if len(args) == 0 || args[0] != "fetch-installer" {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return fetchInstallerCmd(ctx, args[1:], os.Stdout, os.Stderr)
}

// fetchInstallerCmd is `vos ext star-citizen fetch-installer --prefix DIR`,
// run as vapor by Install: it downloads the installer latest.yml names into
// DIR/installer. When it refuses, its line on stdout, after the detail on
// stderr, is the code (fetchCodes) that Install shows as its sentence.
func fetchInstallerCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fetch-installer", flag.ContinueOnError)
	fs.SetOutput(stderr)
	prefix := fs.String("prefix", "", "the Proton prefix Install made")
	if err := fs.Parse(args); err != nil || *prefix == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if os.Geteuid() == 0 && !runAsRootOK {
		fmt.Fprintln(stderr, "vos ext star-citizen fetch-installer runs as vapor, not as root.")
		return 1
	}
	refused := func(err error) int {
		fmt.Fprintln(stderr, "fetch-installer:", err)
		b, _ := json.Marshal(fetchResult{Refused: codeOf(err)})
		fmt.Fprintln(stdout, string(b))
		return 1
	}
	if err := recorded(*prefix); err != nil {
		return refused(err)
	}
	dir := filepath.Join(*prefix, installerDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return refused(extensions.Refuse(codeCantWrite, err))
	}
	r, err := readFeed(ctx)
	if err != nil {
		return refused(extensions.Refuse(codeFeed, fmt.Errorf("reading the RSI Launcher's release: %w", err)))
	}
	if err := fetchInstaller(ctx, r, dir); err != nil {
		code := codeDownload
		if errors.Is(err, errMismatch) {
			code = codeMismatch
		}
		return refused(extensions.Refuse(code, fmt.Errorf("downloading %s: %w", installerURL(r.File), err)))
	}
	b, _ := json.Marshal(fetchResult{Installer: r.File, Version: r.Version})
	fmt.Fprintln(stdout, string(b))
	return 0
}

// runAsRootOK lets tests, which may run as root, call the command.
var runAsRootOK = false
