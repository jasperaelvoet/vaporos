// Package extensions runs VaporOS extensions on the box: the store
// reconcile in vosd, the Steam integration and each extension's helper
// (docs/CONTRACTS.md "Extensions"). `vos ext <command>` lands here; each
// command registers itself from its own file.
package extensions

import (
	"fmt"
	"os"
	"sort"
)

type command struct {
	usage string
	run   func(args []string) int
}

var commands = map[string]command{}

// register adds `vos ext <name>`. Called from init functions.
func register(name, usage string, run func(args []string) int) {
	if _, dup := commands[name]; dup {
		panic("vos ext " + name + " registered twice")
	}
	commands[name] = command{usage: usage, run: run}
}

// CLI runs `vos ext <command> [args]`.
func CLI(args []string) int {
	if len(args) == 0 {
		cliUsage()
		return 2
	}
	c, ok := commands[args[0]]
	if !ok {
		cliUsage()
		return 2
	}
	return c.run(args[1:])
}

func cliUsage() {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintln(os.Stderr, "usage: vos ext <command> [options]")
	fmt.Fprintln(os.Stderr)
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %-12s %s\n", n, commands[n].usage)
	}
}
