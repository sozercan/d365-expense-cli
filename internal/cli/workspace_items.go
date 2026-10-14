package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/sozercan/d365-expense-cli/internal/expense"
)

// workspaceFlags are shared by the workspace Expenses and Receipts commands.
type workspaceFlags struct {
	report reportFlags
}

func newWorkspaceFlags(name string, stderr io.Writer) workspaceFlags {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	report := reportFlags{
		flags:       flags,
		harPath:     flags.String("har", "", "path to a private raw HAR capture"),
		sessionName: flags.String("session", "", "named imported standalone session"),
		execute:     flags.Bool("execute", false, "perform the operation"),
		timeout:     flags.Duration("timeout", 5*time.Minute, "overall execution timeout"),
	}
	return workspaceFlags{report: report}
}

func (workspace workspaceFlags) parse(name string, args []string, stderr io.Writer) (*expense.Client, int) {
	report := workspace.report
	if err := report.flags.Parse(args); err != nil {
		return nil, 2
	}
	if report.flags.NArg() != 0 {
		fmt.Fprintf(stderr, "%s does not accept positional arguments\n", name)
		return nil, 2
	}
	if err := validateProfileSource(name, *report.harPath, *report.sessionName); err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 2
	}
	profile, err := loadBootstrapForRead(*report.harPath, *report.sessionName)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return nil, 1
	}
	client, err := expense.NewFromBootstrap(profile)
	if err == nil {
		err = client.CheckWorkspaceTabs()
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return nil, 1
	}
	return client, 0
}

func runListExpenses(args []string, stdout, stderr io.Writer) int {
	const name = "expense list"
	workspace := newWorkspaceFlags(name, stderr)
	client, code := workspace.parse(name, args, stderr)
	if client == nil {
		return code
	}
	return workspace.report.run(name, client, stderr, func(ctx context.Context, client *expense.Client) error {
		expenses, err := client.ListUnattachedExpenses(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "unattached expenses: %d\n", len(expenses))
		for _, item := range expenses {
			receipts := "no"
			if item.Receipts {
				receipts = "yes"
			}
			fmt.Fprintf(stdout, "%3d  %s  %-24s %16s  merchant=%q receipts=%s\n", item.Number, item.Date, item.Category, item.Amount, item.Merchant, receipts)
		}
		return nil
	})
}

func runDeleteWorkspaceItems(name, label, keyFlag string, args []string, stdout, stderr io.Writer,
	remove func(*expense.Client, context.Context, string, int) (int, error)) int {
	workspace := newWorkspaceFlags(name, stderr)
	key := workspace.report.flags.String(keyFlag, "", "exact "+keyFlag+" of the items to delete")
	count := workspace.report.flags.Int("count", 0, "number of matching items; must match exactly")
	client, code := workspace.parse(name, args, stderr)
	if client == nil {
		return code
	}
	if *key == "" || *count < 1 {
		fmt.Fprintf(stderr, "%s requires --%s and a positive --count\n", name, keyFlag)
		return 2
	}
	if !*workspace.report.execute {
		fmt.Fprintln(stdout, "dry run: no network requests sent; imported session state was not modified")
		fmt.Fprintf(stdout, "- open the workspace %s tab and require exactly %d %ss with %s %q\n", label, *count, label, keyFlag, *key)
		fmt.Fprintln(stdout, "- for each: make it the active row, click Delete, confirm Yes, and refresh to verify exactly that item is gone")
		fmt.Fprintln(stdout, "- return the workspace to its Reports tab")
		return 0
	}
	return workspace.report.run(name, client, stderr, func(ctx context.Context, client *expense.Client) error {
		removed, err := remove(client, ctx, *key, *count)
		if removed != 0 {
			fmt.Fprintf(stdout, "deleted %d %ss with %s %q\n", removed, label, keyFlag, *key)
		}
		return err
	})
}

func runDeleteExpenses(args []string, stdout, stderr io.Writer) int {
	return runDeleteWorkspaceItems("expense delete", "unattached expense", "merchant", args, stdout, stderr,
		(*expense.Client).DeleteUnattachedExpenses)
}

func runDeleteReceipts(args []string, stdout, stderr io.Writer) int {
	return runDeleteWorkspaceItems("receipt delete", "unattached receipt", "name", args, stdout, stderr,
		(*expense.Client).DeleteUnattachedReceipts)
}
