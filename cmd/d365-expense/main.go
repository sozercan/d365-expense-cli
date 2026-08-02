package main

import (
	"os"

	"github.com/sozercan/d365-expense-cli/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
