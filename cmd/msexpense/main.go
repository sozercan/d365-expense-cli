package main

import (
	"fmt"
	"os"

	"github.com/sozercan/d365-expense-cli/internal/cli"
)

func main() {
	fmt.Fprintln(os.Stderr, "warning: msexpense is deprecated; use d365-expense")
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
