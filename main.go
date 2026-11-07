// Command ccs switches between Claude Code accounts.
package main

import (
	"os"

	"github.com/n4ndp/ccs/internal/cli"
	"github.com/n4ndp/ccs/internal/tui"
)

func main() {
	os.Exit(cli.Execute(func(d cli.Deps) func() error {
		return func() error { return tui.Run(d.Store, d.Claude) }
	}))
}
