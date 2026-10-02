// Command vos is the whole VaporOS control plane in one static binary.
// See docs/CONTRACTS.md for every subcommand.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/daemon"
	"github.com/jasperaelvoet/vaporos/internal/display"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/install"
	"github.com/jasperaelvoet/vaporos/internal/session"
	"github.com/jasperaelvoet/vaporos/internal/steamprep"
	"github.com/jasperaelvoet/vaporos/internal/storage"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

var (
	version = "dev"
	commit  = ""
)

func main() {
	config.BinaryVersion, config.BinaryCommit = version, commit

	// systemd runs generators by their own name.
	if filepath.Base(os.Args[0]) == "vos-generator" {
		os.Exit(storage.CLIGenerator(os.Args[1:]))
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "daemon":
		os.Exit(daemon.Main(args))
	case "welcome":
		os.Exit(display.CLIWelcome(args))
	case "session":
		os.Exit(session.CLI(args))
	case "edid":
		os.Exit(display.CLIEdid(args))
	case "install":
		os.Exit(install.CLI(args))
	case "update", "rollback", "status", "health", "sign", "keygen":
		os.Exit(update.CLI(cmd, args))
	case "generator":
		os.Exit(storage.CLIGenerator(args))
	case "ext":
		os.Exit(extensions.CLI(args))
	case "steam":
		os.Exit(steamprep.CLI(args))
	case "version", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: vos <command> [options]

  status [--json]            running version, booted slot, both slots
  update [--from SRC]        fetch, verify and write the newest image to the idle slot
         [--force] [--stage-only]
  rollback                   boot the other slot from now on
  install --disk D ...       install from the live ISO (the web installer does the same)
  daemon                     vosd: web UI, API, updates, display policy
  welcome                    draw the welcome screen on connected monitors
  session begin|end          Sunshine prep-cmd hooks
  edid generate|decode       virtual display EDID
  health                     boot health check
  sign | keygen              update signing
  ext <command>              extensions: fetch, launch, and the build's checks
  steam prepare [--unwrap]   set up Steam for the mounted extensions (before Steam starts)
  version
`)
}
