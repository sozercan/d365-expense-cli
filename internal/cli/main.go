package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/expense"
)

func runLegacy(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printLegacyUsage(stderr)
		return 2
	}

	switch args[0] {
	case "help", "-h", "--help":
		printLegacyUsage(stdout)
		return 0
	case "capture-draft":
		return runCaptureDraft(args[1:], stdout, stderr)
	case "session":
		return runSession(args[1:], stdout, stderr)
	case "inspect":
		return runInspect(args[1:], stdout, stderr)
	case "create-draft":
		return runCreateDraft(args[1:], stdout, stderr)
	case "create-draft-with-receipt":
		return runCreateDraftWithReceipt(args[1:], stdout, stderr)
	case "create-draft-with-receipts":
		return runCreateDraftWithReceipts(args[1:], stdout, stderr)
	case "attach-receipt":
		return runAttachReceipt(args[1:], stdout, stderr)
	case "submit":
		fmt.Fprintln(stderr, "submit is the default create outcome; use d365-expense create")
		return 2
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printLegacyUsage(stderr)
		return 2
	}
}

func runInspect(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	harPath := flags.String("har", "", "path to a private raw HAR capture")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "inspect does not accept positional arguments")
		return 2
	}
	if *harPath == "" {
		fmt.Fprintln(stderr, "inspect requires --har")
		return 2
	}
	if err := requirePrivateCapture(*harPath); err != nil {
		fmt.Fprintf(stderr, "inspect: %v\n", err)
		return 1
	}

	profile, err := capture.Load(*harPath)
	if err != nil {
		fmt.Fprintf(stderr, "inspect: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, profile.SafeSummary())
	return 0
}

func runCreateDraft(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("create-draft", flag.ContinueOnError)
	flags.SetOutput(stderr)
	harPath := flags.String("har", "", "path to a private raw HAR capture")
	sessionName := flags.String("session", "", "named imported standalone session")
	purpose := flags.String("purpose", "", "expense report title/purpose")
	submit := flags.Bool("submit", false, "submit the newly created report instead of saving it as a Draft")
	execute := flags.Bool("execute", false, "send the three allowlisted report-creation requests")
	timeout := flags.Duration("timeout", 45*time.Second, "overall execution timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "create-draft does not accept positional arguments")
		return 2
	}
	if err := validateProfileSource("create-draft", *harPath, *sessionName); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *purpose == "" {
		fmt.Fprintln(stderr, "create-draft requires --purpose")
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "create-draft requires a positive --timeout")
		return 2
	}
	profile, err := loadBootstrapForRead(*harPath, *sessionName)
	if err != nil {
		fmt.Fprintf(stderr, "create-draft: %v\n", err)
		return 1
	}
	client, err := expense.NewFromBootstrap(profile)
	if err != nil {
		fmt.Fprintf(stderr, "create-draft: %v\n", err)
		return 1
	}
	finalAction := expense.ReportFinalActionSaveDraft
	if *submit {
		finalAction = expense.ReportFinalActionSubmit
	}
	reportRequest := expense.CreateReportRequest{Purpose: *purpose, FinalAction: finalAction}
	plan, err := client.PlanCreateReport(reportRequest)
	if err != nil {
		fmt.Fprintf(stderr, "create-draft: %v\n", err)
		return 1
	}

	if !*execute {
		fmt.Fprintln(stdout, "dry run: no network requests sent; imported session state was not modified")
		fmt.Fprintf(stdout, "purpose: %q\n", plan.Purpose)
		fmt.Fprintf(stdout, "requests: %d\n", plan.RequestCount)
		for _, action := range plan.Actions {
			fmt.Fprintf(stdout, "- %s\n", action)
		}
		if *submit {
			fmt.Fprintln(stdout, "rerun with --execute to create and submit the report")
		} else {
			fmt.Fprintln(stdout, "rerun with --execute to create and save the Draft report")
		}
		return 0
	}

	var execution *namedSessionExecution
	if *sessionName != "" {
		execution, err = beginNamedSessionExecution(*sessionName)
		if err != nil {
			fmt.Fprintf(stderr, "create-draft: %v\n", err)
			return 1
		}
		client = execution.client
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	report, operationErr := client.CreateReport(ctx, reportRequest)
	var checkpointErr error
	if execution != nil {
		checkpointErr = execution.finish(operationErr)
	}
	if operationErr != nil {
		writeCreateReportOperationFailure(stderr, *harPath, *submit, operationErr, checkpointErr)
		return 1
	}
	if report.Submitted {
		fmt.Fprintf(stdout, "created and submitted report %s: purpose=%q status=%s submitted=true\n",
			report.ReportNumber, report.Purpose, report.Status)
	} else {
		fmt.Fprintf(stdout, "created draft %s: purpose=%q status=%s saved-and-closed=%t\n",
			report.ReportNumber, report.Purpose, report.Status, report.SavedAndClosed)
	}
	if checkpointErr != nil {
		fmt.Fprintf(stderr, "create-draft: report operation succeeded, but session checkpoint failed; do not retry this expense operation: %v\n", checkpointErr)
		return 1
	}
	return 0
}

func writeCreateReportOperationFailure(stderr io.Writer, harPath string, submit bool, operationErr, checkpointErr error) {
	fmt.Fprintf(stderr, "create-draft: %v\n", operationErr)
	if errors.Is(operationErr, expense.ErrOperationUncertain) {
		outcome := "created or saved"
		if submit {
			outcome = "created or submitted"
		}
		if harPath != "" {
			fmt.Fprintf(stderr, "the report may already have been %s and its final state may be uncertain; do not retry with the same HAR\n", outcome)
		} else {
			fmt.Fprintf(stderr, "the report may already have been %s and its final state may be uncertain; verify Dynamics and do not re-import and retry this expense operation\n", outcome)
		}
	}
	if checkpointErr != nil {
		fmt.Fprintf(stderr, "create-draft: session checkpoint also failed: %v\n", checkpointErr)
	}
}

func runAttachReceipt(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("attach-receipt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	harPath := flags.String("har", "", "path to a private receipt-capable HAR capture")
	reportNumber := flags.String("report", "", "captured Draft expense report number")
	filePath := flags.String("file", "", "private PNG receipt file")
	notes := flags.String("notes", "", "optional receipt notes")
	execute := flags.Bool("execute", false, "send the allowlisted receipt attachment requests")
	timeout := flags.Duration("timeout", 90*time.Second, "overall execution timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "attach-receipt does not accept positional arguments")
		return 2
	}
	if *harPath == "" || *reportNumber == "" || *filePath == "" {
		fmt.Fprintln(stderr, "attach-receipt requires --har, --report, and --file")
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "attach-receipt requires a positive --timeout")
		return 2
	}
	if err := requirePrivateCapture(*harPath); err != nil {
		fmt.Fprintf(stderr, "attach-receipt: %v\n", err)
		return 1
	}

	profile, err := capture.LoadReceipt(*harPath)
	if err != nil {
		fmt.Fprintf(stderr, "attach-receipt: %v\n", err)
		return 1
	}
	input, err := receiptInputFromPath(*filePath, profile.Upload.MaxSupportedSingleFileSize)
	if err != nil {
		fmt.Fprintf(stderr, "attach-receipt: %v\n", err)
		return 1
	}
	client, err := expense.NewReceiptClient(profile)
	if err != nil {
		fmt.Fprintf(stderr, "attach-receipt: %v\n", err)
		return 1
	}
	request := expense.AttachReceiptRequest{
		ReportNumber: *reportNumber,
		Notes:        *notes,
		Receipt:      input,
	}
	plan, err := client.PlanAttachReceipt(request)
	if err != nil {
		fmt.Fprintf(stderr, "attach-receipt: %v\n", err)
		return 1
	}

	if !*execute {
		fmt.Fprintln(stdout, "dry run: no network requests sent; the receipt was read only for local validation")
		fmt.Fprintf(stdout, "report: %s\n", plan.ReportNumber)
		fmt.Fprintf(stdout, "receipt: %q (%s, %d bytes)\n", plan.Receipt.Filename, plan.Receipt.MediaType, plan.Receipt.Size)
		fmt.Fprintf(stdout, "requests: %d\n", plan.RequestCount)
		for _, action := range plan.Actions {
			fmt.Fprintf(stdout, "- %s\n", action)
		}
		fmt.Fprintln(stdout, "rerun with --execute to attach the receipt and save the Draft report")
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	result, err := client.AttachReceipt(ctx, request)
	if err != nil {
		fmt.Fprintf(stderr, "attach-receipt: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "attached receipt %q to draft %s: status=%s receipt-count=%d saved-and-closed=%t\n",
		result.Attached.Filename, result.ReportNumber, result.Status, result.ReceiptCount, result.SavedAndClosed)
	return 0
}

func requirePrivateCapture(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat HAR: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("HAR path is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("HAR permissions %04o are too broad; run chmod 600 %q", info.Mode().Perm(), path)
	}
	return nil
}

func printLegacyUsage(w io.Writer) {
	fmt.Fprintln(w, "msexpense: create Dynamics 365 expense reports from an imported browser session")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  msexpense session import --name <name> <workspace.har>")
	fmt.Fprintln(w, "  msexpense session import-cdp --name <name> <expense-workspace-url>")
	fmt.Fprintln(w, "  msexpense session list|inspect|remove|unlock")
	fmt.Fprintln(w, "  msexpense capture-draft --out <workspace.har> <expense-workspace-url>")
	fmt.Fprintln(w, "    (optional helper: captures workspace state only; it does not create a Draft)")
	fmt.Fprintln(w, "  msexpense inspect --har <capture.har>")
	fmt.Fprintln(w, "  msexpense create-draft (--session <name> | --har <capture.har>) --purpose <text> [--execute]")
	fmt.Fprintln(w, "  msexpense create-draft-with-receipts (--session <name> | --har <capture.har>) --purpose <text> --file <receipt.png> [--file <receipt.png> ...] [--execute]")
	fmt.Fprintln(w, "    (singular create-draft-with-receipt is a compatibility alias)")
	fmt.Fprintln(w, "  msexpense attach-receipt --har <receipt.har> --report <number> --file <receipt.png> [--execute]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Create and attach commands are dry runs unless --execute is supplied.")
	fmt.Fprintln(w, "Imported sessions are browser-free but expire when Dynamics revokes their credentials. Canonical d365-expense create submits by default; add --draft to opt out.")
}
