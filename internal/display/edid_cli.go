package display

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

// CLIEdid is `vos edid generate|decode`.
func CLIEdid(args []string) int { return cliEdid(args, os.Stdout, os.Stderr) }

func cliEdid(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprint(stderr, `usage:
  vos edid generate --out FILE [--modes-from clients.json] [--extra WxH@R ...]
  vos edid decode FILE
`)
		return 2
	}
	if len(args) < 1 {
		return usage()
	}
	switch args[0] {
	case "generate":
		return edidGenerate(args[1:], stdout, stderr)
	case "decode":
		if len(args) != 2 {
			return usage()
		}
		b, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Fprintln(stderr, "vos edid:", err)
			return 1
		}
		info, err := edid.Decode(b)
		if err != nil {
			fmt.Fprintln(stderr, "vos edid:", err)
			return 1
		}
		fmt.Fprint(stdout, info.Format())
		return 0
	default:
		return usage()
	}
}

// modeList is a repeatable --extra flag that also accepts comma lists.
type modeList []edid.Mode

func (l *modeList) String() string {
	var s []string
	for _, m := range *l {
		s = append(s, m.String())
	}
	return strings.Join(s, ",")
}

func (l *modeList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		m, err := edid.ParseMode(part)
		if err != nil {
			return err
		}
		*l = append(*l, m)
	}
	return nil
}

func edidGenerate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vos edid generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "file to write")
	from := fs.String("modes-from", "", "clients.json to take learned modes from")
	var extra modeList
	fs.Var(&extra, "extra", "extra mode WxH@R (repeatable, or comma separated)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	modes := []edid.Mode(extra)
	if *from != "" {
		c, err := LoadClients(*from)
		if err != nil {
			fmt.Fprintln(stderr, "vos edid:", err)
			return 1
		}
		modes = append(modes, c.Modes()...)
	}
	res, err := edid.Generate(modes)
	if err != nil {
		fmt.Fprintln(stderr, "vos edid:", err)
		return 1
	}
	for _, e := range res.Skipped {
		fmt.Fprintln(stderr, "vos edid: skipped", e)
	}
	if err := config.WriteFileAtomic(*out, res.EDID, 0o644); err != nil {
		fmt.Fprintln(stderr, "vos edid:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s: %d blocks, %d modes\n", *out, len(res.EDID)/128, len(res.Modes))
	return 0
}
