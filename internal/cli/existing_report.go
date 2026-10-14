package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/sozercan/d365-expense-cli/internal/expense"
)

// reportFlags are shared by the existing-report commands.
type reportFlags struct {
	flags        *flag.FlagSet
	harPath      *string
	sessionName  *string
	reportNumber *string
	execute      *bool
	timeout      *time.Duration
}

func newReportFlags(name string, stderr io.Writer, defaultTimeout time.Duration) reportFlags {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return reportFlags{
		flags:        flags,
		harPath:      flags.String("har", "", "path to a private raw HAR capture"),
		sessionName:  flags.String("session", "", "named imported standalone session"),
		reportNumber: flags.String("report", "", "Draft expense report number"),
		execute:      flags.Bool("execute", false, "perform the operation"),
		timeout:      flags.Duration("timeout", defaultTimeout, "overall execution timeout"),
	}
}

// parse returns a client for the selected source, or nil and an exit code.
func (report reportFlags) parse(name string, args []string, stderr io.Writer) (*expense.Client, int) {
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
	if *report.reportNumber == "" || *report.timeout <= 0 {
		fmt.Fprintf(stderr, "%s requires --report and a positive --timeout\n", name)
		return nil, 2
	}
	profile, err := loadBootstrapForRead(*report.harPath, *report.sessionName)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return nil, 1
	}
	client, err := expense.NewFromBootstrap(profile)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return nil, 1
	}
	// Fail before locking a named session when it cannot open the report.
	if err := client.CheckExistingReport(*report.reportNumber); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return nil, 1
	}
	return client, 0
}

// run executes operation, using a named session's client when one is
// selected, then checkpoints the session and reports failures.
func (report reportFlags) run(name string, client *expense.Client, stderr io.Writer, operation func(context.Context, *expense.Client) error) int {
	var execution *namedSessionExecution
	if *report.sessionName != "" {
		var err error
		if execution, err = beginNamedSessionExecution(*report.sessionName); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return 1
		}
		client = execution.client
	}
	ctx, cancel := context.WithTimeout(context.Background(), *report.timeout)
	defer cancel()
	operationErr := operation(ctx, client)
	var checkpointErr error
	if execution != nil {
		checkpointErr = execution.finish(operationErr)
	}
	if operationErr != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, operationErr)
		if errors.Is(operationErr, expense.ErrOperationUncertain) {
			fmt.Fprintln(stderr, "the report may already have changed; verify Dynamics and do not retry with the same session or HAR")
		}
		if checkpointErr != nil {
			fmt.Fprintf(stderr, "%s: session checkpoint also failed: %v\n", name, checkpointErr)
		}
		return 1
	}
	if checkpointErr != nil {
		fmt.Fprintf(stderr, "%s: operation succeeded, but session checkpoint failed; re-import before the next operation: %v\n", name, checkpointErr)
		return 1
	}
	return 0
}

func printReportPlan(stdout io.Writer, reportNumber string, actions []string) {
	fmt.Fprintln(stdout, "dry run: no network requests sent; imported session state was not modified")
	fmt.Fprintf(stdout, "report: %s\n", reportNumber)
	for _, action := range actions {
		fmt.Fprintf(stdout, "- %s\n", action)
	}
}

func runDeleteReport(args []string, stdout, stderr io.Writer) int {
	const name = "report delete"
	report := newReportFlags(name, stderr, defaultDeleteTimeout)
	client, code := report.parse(name, args, stderr)
	if client == nil {
		return code
	}
	request := expense.DeleteReportRequest{ReportNumber: *report.reportNumber}
	plan, err := client.PlanDeleteDraftReport(request)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	if !*report.execute {
		printReportPlan(stdout, plan.ReportNumber, plan.Actions)
		return 0
	}
	return report.run(name, client, stderr, func(ctx context.Context, client *expense.Client) error {
		result, err := client.DeleteDraftReport(ctx, request)
		if err == nil {
			fmt.Fprintf(stdout, "deleted draft %s: purpose=%q\n", result.ReportNumber, result.Purpose)
			fmt.Fprintln(stdout, "Dynamics keeps its expense lines as unattached expenses")
		}
		return err
	})
}

func runShowReport(args []string, stdout, stderr io.Writer) int {
	const name = "report show"
	report := newReportFlags(name, stderr, 2*time.Minute)
	client, code := report.parse(name, args, stderr)
	if client == nil {
		return code
	}
	return report.run(name, client, stderr, func(ctx context.Context, client *expense.Client) error {
		details, err := client.ShowDraftReport(ctx, *report.reportNumber)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "report %s: purpose=%q status=%s lines=%d\n", details.ReportNumber, details.Purpose, details.Status, len(details.Lines))
		for _, line := range details.Lines {
			receipts := "no"
			if line.Receipts {
				receipts = "yes"
			}
			fmt.Fprintf(stdout, "%3d  %s  %-24s %16s  merchant=%q receipts=%s", line.Number, line.Date, line.Category, line.Amount, line.Merchant, receipts)
			if line.Notes != "" {
				fmt.Fprintf(stdout, " notes=%q", line.Notes)
			}
			fmt.Fprintln(stdout)
		}
		return nil
	})
}

func runItemizeReportLine(args []string, stdout, stderr io.Writer) int {
	const name = "report itemize"
	report := newReportFlags(name, stderr, 5*time.Minute)
	line := report.flags.Int("line", 0, "line number shown by report show")
	itemizationPath := report.flags.String("itemization", "", "JSON file of itemization rows")
	client, code := report.parse(name, args, stderr)
	if client == nil {
		return code
	}
	if *line < 1 || *itemizationPath == "" {
		fmt.Fprintf(stderr, "%s requires --line and --itemization\n", name)
		return 2
	}
	rows, err := loadItemizationFile(*itemizationPath)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	request := expense.ItemizeReportLineRequest{ReportNumber: *report.reportNumber, Line: *line, Itemization: rows}
	if !*report.execute {
		printReportPlan(stdout, request.ReportNumber, []string{
			fmt.Sprintf("open the Draft and select line %d", request.Line),
			fmt.Sprintf("itemize it into %d rows that must add up exactly to the line amount", len(rows)),
			"verify nothing remains to itemize and the itemized lines were created",
			"save and close the Draft",
		})
		for index, row := range rows {
			fmt.Fprintf(stdout, "  row %d: %s %s x %d days", index+1, row.Subcategory, row.DailyRate, row.Days)
			if row.StartDate != "" {
				fmt.Fprintf(stdout, " from %s", row.StartDate)
			}
			fmt.Fprintln(stdout)
		}
		return 0
	}
	return report.run(name, client, stderr, func(ctx context.Context, client *expense.Client) error {
		result, err := client.ItemizeDraftReportLine(ctx, request)
		if err == nil {
			fmt.Fprintf(stdout, "itemized line %d of draft %s: %s %s %s, itemized-lines=%d\n",
				request.Line, request.ReportNumber, result.Date, result.Category, result.Amount, result.ItemizedLines)
		}
		return err
	})
}

func runSubmitReport(args []string, stdout, stderr io.Writer) int {
	const name = "report submit"
	report := newReportFlags(name, stderr, 3*time.Minute)
	client, code := report.parse(name, args, stderr)
	if client == nil {
		return code
	}
	if !*report.execute {
		printReportPlan(stdout, *report.reportNumber, []string{
			"open the Draft",
			"click only its validated SubmitButton",
			"refresh the reports grid and verify the report left Draft status",
		})
		return 0
	}
	return report.run(name, client, stderr, func(ctx context.Context, client *expense.Client) error {
		result, err := client.SubmitDraftReport(ctx, *report.reportNumber)
		if err == nil {
			fmt.Fprintf(stdout, "submitted report %s: purpose=%q approval-status-code=%s\n", result.ReportNumber, result.Purpose, result.Status)
		}
		return err
	})
}

func runAttachReportReceipt(args []string, stdout, stderr io.Writer) int {
	const name = "report attach-receipt"
	report := newReportFlags(name, stderr, 3*time.Minute)
	line := report.flags.Int("line", 0, "line number shown by report show")
	receipt := report.flags.String("receipt", "", "receipt file name as listed on the report")
	client, code := report.parse(name, args, stderr)
	if client == nil {
		return code
	}
	if *line < 1 || *receipt == "" {
		fmt.Fprintf(stderr, "%s requires --line and --receipt\n", name)
		return 2
	}
	request := expense.AttachReportReceiptRequest{ReportNumber: *report.reportNumber, Line: *line, Receipt: *receipt}
	if !*report.execute {
		printReportPlan(stdout, request.ReportNumber, []string{
			fmt.Sprintf("open the Draft and select receipt %q on its Receipts tab", request.Receipt),
			fmt.Sprintf("open Attach to expense and select the offered line matching line %d by date, amount, and merchant", request.Line),
			"attach, verify Dynamics recorded the match and stopped offering the line, and close the popup",
			"save and close the Draft",
		})
		return 0
	}
	return report.run(name, client, stderr, func(ctx context.Context, client *expense.Client) error {
		result, err := client.AttachReportReceipt(ctx, request)
		if err == nil {
			fmt.Fprintf(stdout, "attached receipt %q to line %d of draft %s: %s %s %s\n",
				result.Receipt, request.Line, request.ReportNumber, result.Date, result.Category, result.Amount)
		}
		return err
	})
}

func runRemoveReportReceipts(args []string, stdout, stderr io.Writer) int {
	const name = "report remove-receipt"
	report := newReportFlags(name, stderr, 5*time.Minute)
	receipt := report.flags.String("receipt", "", "receipt file name as listed on the report")
	count := report.flags.Int("count", 1, "number of receipts with that name to remove")
	client, code := report.parse(name, args, stderr)
	if client == nil {
		return code
	}
	if *receipt == "" || *count < 1 {
		fmt.Fprintf(stderr, "%s requires --receipt and a positive --count\n", name)
		return 2
	}
	request := expense.RemoveReportReceiptsRequest{ReportNumber: *report.reportNumber, Receipt: *receipt, Count: *count}
	if !*report.execute {
		printReportPlan(stdout, request.ReportNumber, []string{
			fmt.Sprintf("open the Draft and require exactly %d receipts named %q, none attached to an expense line", request.Count, request.Receipt),
			"for each: select it, click Remove, confirm Yes, and verify exactly that receipt is gone",
			"save and close the Draft",
		})
		return 0
	}
	return report.run(name, client, stderr, func(ctx context.Context, client *expense.Client) error {
		removed, err := client.RemoveReportReceipts(ctx, request)
		if removed != 0 {
			fmt.Fprintf(stdout, "removed %d receipts named %q from draft %s\n", removed, request.Receipt, request.ReportNumber)
		}
		return err
	})
}
