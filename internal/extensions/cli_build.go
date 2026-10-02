package extensions

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/extensions/buildcheck"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/extensions/fsverity"
)

// The build's commands (build/extensions.sh): the arguments are its
// interface, so they change only together with it.
func init() {
	register("check-tree", "--id ID --descriptor FILE --tree DIR --base DIR [--other DIR]... [--json OUT]: check an image tree (build)",
		func(args []string) int { return checkTreeCmd(args, os.Stderr) })
	register("catalog", "--stage DIR --out DIR: write the catalog, manifest entries and descriptors (build)",
		func(args []string) int { return catalogCmd(args, os.Stderr) })
	register("digest", "FILE...: print each file's fs-verity digest",
		func(args []string) int { return digestCmd(args, os.Stdout, os.Stderr) })
}

// dirList is a repeatable directory flag.
type dirList []string

func (d *dirList) String() string     { return strings.Join(*d, ",") }
func (d *dirList) Set(v string) error { *d = append(*d, v); return nil }

func checkTreeCmd(args []string, stderr io.Writer) int {
	f := flag.NewFlagSet("vos ext check-tree", flag.ContinueOnError)
	f.SetOutput(stderr)
	id := f.String("id", "", "the extension's id")
	desc := f.String("descriptor", "", "its source descriptor (extension.json)")
	tree := f.String("tree", "", "the image root, holding usr/")
	base := f.String("base", "", "the assembled OS root")
	jsonOut := f.String("json", "", "write the verified permissions, root and warnings here")
	var others dirList
	f.Var(&others, "other", "the tree of an extension built before it (repeatable)")
	if err := f.Parse(args); err != nil {
		return 2
	}
	if f.NArg() > 0 || *id == "" || *desc == "" || *tree == "" || *base == "" {
		fmt.Fprintln(stderr, "usage: vos ext check-tree --id ID --descriptor FILE --tree DIR --base DIR [--other DIR]... [--json OUT]")
		return 2
	}
	d, err := descriptor.Load(*desc)
	if err == nil {
		err = d.ValidateSource()
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", *id, err)
		return 1
	}
	r := buildcheck.CheckTree(buildcheck.Options{ID: *id, Descriptor: d, Tree: *tree, Base: *base, Others: others})
	for _, w := range r.Warnings {
		fmt.Fprintf(stderr, "%s: warning: %s\n", *id, w)
	}
	for _, p := range r.Problems {
		fmt.Fprintf(stderr, "%s: %s\n", *id, p)
	}
	if len(r.Problems) > 0 {
		return 1
	}
	if *jsonOut != "" {
		b, err := json.MarshalIndent(r.Result, "", "  ")
		if err == nil {
			err = os.WriteFile(*jsonOut, append(b, '\n'), 0o644)
		}
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", *id, err)
			return 1
		}
	}
	return 0
}

func catalogCmd(args []string, stderr io.Writer) int {
	f := flag.NewFlagSet("vos ext catalog", flag.ContinueOnError)
	f.SetOutput(stderr)
	stage := f.String("stage", "", "the images, descriptors and check results")
	out := f.String("out", "", "where to write extensions.list, extensions.json and descriptors/")
	if err := f.Parse(args); err != nil {
		return 2
	}
	if f.NArg() > 0 || *stage == "" || *out == "" {
		fmt.Fprintln(stderr, "usage: vos ext catalog --stage DIR --out DIR")
		return 2
	}
	s, err := buildcheck.ReadStage(*stage)
	if err == nil {
		err = s.Write(*out)
	}
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			fmt.Fprintln(stderr, "catalog:", line)
		}
		return 1
	}
	return 0
}

// digestCmd prints "<digest>  <file>" like sha256sum, so the build can
// compare it with fsverity-utils.
func digestCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: vos ext digest FILE...")
		return 2
	}
	rc := 0
	for _, file := range args {
		d, err := fsverity.DigestFile(file)
		if err != nil {
			fmt.Fprintln(stderr, err)
			rc = 1
			continue
		}
		fmt.Fprintf(stdout, "%s  %s\n", d, file)
	}
	return rc
}
