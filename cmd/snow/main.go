// Command snow is a terminal UI for reviewing GitHub pull requests.
package main

import (
	"os"

	"github.com/chrisdaly-3/jawnsnow/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
