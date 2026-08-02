package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	sessionstore "github.com/sozercan/d365-expense-cli/internal/session"
)

func runSession(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printSessionUsage(stdout)
		return 0
	}
	switch args[0] {
	case "import":
		return runSessionImport(args[1:], stdout, stderr)
	case "import-cdp":
		return runSessionImportCDP(args[1:], stdout, stderr)
	case "list":
		return runSessionList(args[1:], stdout, stderr)
	case "inspect":
		return runSessionInspect(args[1:], stdout, stderr)
	case "remove":
		return runSessionRemove(args[1:], stdout, stderr)
	case "unlock":
		return runSessionUnlock(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown session subcommand %q\n", args[0])
		return 2
	}
}

func runSessionImport(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("session import", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "", "session name")
	force := flags.Bool("force", false, "replace an existing named session")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 || *name == "" {
		fmt.Fprintln(stderr, "session import requires --name and exactly one private raw HAR")
		return 2
	}
	harPath := flags.Arg(0)
	if err := requirePrivateCapture(harPath); err != nil {
		fmt.Fprintf(stderr, "session import: %v\n", err)
		return 1
	}
	profile, err := capture.LoadBootstrap(harPath)
	if err != nil {
		fmt.Fprintf(stderr, "session import: %v\n", err)
		return 1
	}
	standalone, err := sessionstore.FromBootstrap(profile)
	if err != nil {
		fmt.Fprintf(stderr, "session import: %v\n", err)
		return 1
	}
	store, err := sessionstore.DefaultStore()
	if err != nil {
		fmt.Fprintf(stderr, "session import: %v\n", err)
		return 1
	}
	lock, err := store.AcquireLock(*name)
	if err != nil {
		fmt.Fprintf(stderr, "session import: %v\n", err)
		return 1
	}
	defer func() { _ = lock.Release() }()

	path, err := store.Path(*name)
	if err != nil {
		fmt.Fprintf(stderr, "session import: %v\n", err)
		return 1
	}
	if _, err := os.Lstat(path); err == nil && !*force {
		fmt.Fprintf(stderr, "session import: session %q already exists; use --force to replace it\n", *name)
		return 1
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "session import: inspect existing session: %v\n", err)
		return 1
	}
	if err := store.Save(*name, standalone); err != nil {
		fmt.Fprintf(stderr, "session import: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "imported session %q\n", *name)
	fmt.Fprintln(stdout, standalone.SafeSummary(*name))
	return 0
}

func runSessionList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("session list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "session list does not accept positional arguments")
		return 2
	}
	store, err := sessionstore.DefaultStore()
	if err != nil {
		fmt.Fprintf(stderr, "session list: %v\n", err)
		return 1
	}
	summaries, err := store.List()
	if err != nil {
		fmt.Fprintf(stderr, "session list: %v\n", err)
		return 1
	}
	if len(summaries) == 0 {
		fmt.Fprintln(stdout, "no imported sessions")
		return 0
	}
	for _, summary := range summaries {
		fmt.Fprintln(stdout, summary.String())
	}
	return 0
}

func runSessionInspect(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("session inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "", "session name")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *name == "" {
		fmt.Fprintln(stderr, "session inspect requires --name")
		return 2
	}
	store, err := sessionstore.DefaultStore()
	if err != nil {
		fmt.Fprintf(stderr, "session inspect: %v\n", err)
		return 1
	}
	summary, err := store.Inspect(*name)
	if err != nil {
		fmt.Fprintf(stderr, "session inspect: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, summary.String())
	return 0
}

func runSessionRemove(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("session remove", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "", "session name")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *name == "" {
		fmt.Fprintln(stderr, "session remove requires --name")
		return 2
	}
	store, err := sessionstore.DefaultStore()
	if err != nil {
		fmt.Fprintf(stderr, "session remove: %v\n", err)
		return 1
	}
	lock, err := store.AcquireLock(*name)
	if err != nil {
		fmt.Fprintf(stderr, "session remove: %v\n", err)
		return 1
	}
	defer func() { _ = lock.Release() }()
	if err := store.Remove(*name); err != nil {
		fmt.Fprintf(stderr, "session remove: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "removed session %q\n", *name)
	return 0
}

func runSessionUnlock(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("session unlock", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "", "session name")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *name == "" {
		fmt.Fprintln(stderr, "session unlock requires --name")
		return 2
	}
	store, err := sessionstore.DefaultStore()
	if err != nil {
		fmt.Fprintf(stderr, "session unlock: %v\n", err)
		return 1
	}
	if err := store.BreakLock(*name); err != nil {
		fmt.Fprintf(stderr, "session unlock: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "removed stale lock for session %q; re-import a fresh HAR before executing\n", *name)
	return 0
}

func printSessionUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  msexpense session import --name <name> [--force] <raw.har>")
	fmt.Fprintln(w, "  msexpense session import-cdp --name <name> [--force] <expense-workspace-url>")
	fmt.Fprintln(w, "  msexpense session list")
	fmt.Fprintln(w, "  msexpense session inspect --name <name>")
	fmt.Fprintln(w, "  msexpense session remove --name <name>")
	fmt.Fprintln(w, "  msexpense session unlock --name <name>  # stale-lock recovery only")
}
