// Command prettytail pretty-renders a file in the terminal and keeps
// formatting it live as it grows. See internal/cli for the CLI contract.
package main

import (
	"os"

	"github.com/The-Bunch/prettytail/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args))
}
