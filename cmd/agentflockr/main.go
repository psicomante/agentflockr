// Command agentflockr is the dashboard's executable. Everything it does lives
// in internal/cli, where it can be tested.
package main

import (
	"os"

	"github.com/psicomante/agentflockr/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
