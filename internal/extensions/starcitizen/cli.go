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
)

const usage = "usage: vos ext star-citizen fetch-installer --prefix DIR"

// fetchResult is fetch-installer's one line on stdout.
type fetchResult struct {
	Installer string `json:"installer"`
	Version   string `json:"version"`
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
// DIR/installer. Its last line on stderr is meant for the person.
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
	if !knownPrefix(*prefix) {
		fmt.Fprintf(stderr, "%s is not a folder Star Citizen uses.\n", *prefix)
		return 1
	}
	if err := checkPrefix(*prefix); err != nil {
		fmt.Fprintln(stderr, "its drive isn't connected. Connect it, then try again.")
		return 1
	}
	dir := filepath.Join(*prefix, installerDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(stderr, "making %s: %v\n", dir, err)
		return 1
	}
	r, err := readFeed(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "reading the RSI Launcher's release: %v\n", err)
		fmt.Fprintln(stderr, "the RSI Launcher's download page didn't answer. Check the internet connection, then try again.")
		return 1
	}
	if err := fetchInstaller(ctx, r, dir); err != nil {
		fmt.Fprintf(stderr, "downloading %s: %v\n", installerURL(r.File), err)
		if errors.Is(err, errMismatch) {
			fmt.Fprintln(stderr, "the RSI Launcher's installer didn't match the fingerprint its publisher lists, so VaporOS deleted it. Try again later.")
		} else {
			fmt.Fprintln(stderr, "the RSI Launcher's installer didn't download. Check the internet connection, then try again.")
		}
		return 1
	}
	b, _ := json.Marshal(fetchResult{Installer: r.File, Version: r.Version})
	fmt.Fprintln(stdout, string(b))
	return 0
}

// runAsRootOK lets tests, which may run as root, call the command.
var runAsRootOK = false
