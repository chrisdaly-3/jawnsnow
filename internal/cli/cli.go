// Package cli implements the snow command line.
package cli

import (
	"fmt"
	"io"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
)

const usage = "usage: snow <command>\n\ncommands:\n  config default  print the commented default config\n  version         print the version\n"

// Run executes the snow command with args (excluding the program name) and
// returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, "snow dev")
		return 0
	case "config":
		if len(args) == 2 && args[1] == "default" {
			if _, err := stdout.Write(config.DefaultTOML()); err != nil {
				fmt.Fprintf(stderr, "snow: write default config: %v\n", err)
				return 1
			}
			return 0
		}
		fmt.Fprint(stderr, usage)
		return 2
	default:
		fmt.Fprintf(stderr, "snow: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
