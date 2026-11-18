// Package cli implements the snow command line.
package cli

import (
	"fmt"
	"io"
)

const usage = "usage: snow <command>\n\ncommands:\n  version  print the version\n"

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
	default:
		fmt.Fprintf(stderr, "snow: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
