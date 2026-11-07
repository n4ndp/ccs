// Command ccs switches between Claude Code accounts.
package main

import (
	"os"

	"github.com/n4ndp/ccs/internal/cli"
)

func main() {
	os.Exit(cli.Execute(nil))
}
