package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/expense"
)

const maxCLIReceipts = 20

type receiptPathList []string

func (paths *receiptPathList) String() string { return strings.Join(*paths, ",") }
func (paths *receiptPathList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("receipt file path must not be empty")
	}
	*paths = append(*paths, value)
	return nil
}

func runCreateDraftWithReceipt(args []string, stdout, stderr io.Writer) int {
	return runCreateDraftWithReceiptsCommand("create-draft-with-receipt", args, stdout, stderr)
}

func runCreateDraftWithReceipts(args []string, stdout, stderr io.Writer) int {
	return runCreateDraftWithReceiptsCommand("create-draft-with-receipts", args, stdout, stderr)
}

func runCreateDraftWithReceiptsCommand(commandName string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(commandName, flag.ContinueOnError)
	flags.SetOutput(stderr)
	harPath := flags.String("har", "", "path to a private raw HAR capture")
	sessionName := flags.String("session", "", "named imported standalone session")
	receiptProtocolHAR := flags.String("receipt-protocol-har", "", "optional legacy receipt HAR used only for its validated upload contract")
	purpose := flags.String("purpose", "", "expense report title/purpose")
	var filePaths receiptPathList
	flags.Var(&filePaths, "file", "private PNG receipt file; repeat for every receipt")
	notes := flags.String("notes", "", "optional notes applied to every receipt")
	submit := flags.Bool("submit", false, "submit the newly created report after attaching every receipt")
	execute := flags.Bool("execute", false, "create the report and perform its requested final action")
	timeout := flags.Duration("timeout", 120*time.Second, "overall execution timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "%s does not accept positional arguments\n", commandName)
		return 2
	}
	if err := validateProfileSource(commandName, *harPath, *sessionName); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *purpose == "" || len(filePaths) == 0 {
		fmt.Fprintf(stderr, "%s requires --purpose and at least one --file\n", commandName)
		return 2
	}
	if len(filePaths) > maxCLIReceipts {
		fmt.Fprintf(stderr, "%s supports at most %d receipts per Draft\n", commandName, maxCLIReceipts)
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintf(stderr, "%s requires a positive --timeout\n", commandName)
		return 2
	}
	contract := expense.BuiltinReceiptUploadContract()
	if *receiptProtocolHAR != "" {
		if err := requirePrivateCapture(*receiptProtocolHAR); err != nil {
			fmt.Fprintf(stderr, "%s: receipt protocol HAR: %v\n", commandName, err)
			return 1
		}
		receiptProfile, err := capture.LoadReceipt(*receiptProtocolHAR)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", commandName, err)
			return 1
		}
		contract, err = expense.ReceiptUploadContractFromProfile(receiptProfile)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", commandName, err)
			return 1
		}
	}
	receipts, err := receiptInputsFromPaths(filePaths, *notes, contract.MaxSupportedSingleFileSize())
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", commandName, err)
		return 1
	}
	profile, err := loadBootstrapForRead(*harPath, *sessionName)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", commandName, err)
		return 1
	}
	client, err := expense.NewFromBootstrap(profile)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", commandName, err)
		return 1
	}
	finalAction := expense.ReportFinalActionSaveDraft
	if *submit {
		finalAction = expense.ReportFinalActionSubmit
	}
	request := expense.CreateReportWithReceiptsRequest{
		Purpose:        *purpose,
		Receipts:       receipts,
		UploadContract: contract,
		FinalAction:    finalAction,
	}
	plan, err := client.PlanCreateReportWithReceipts(request)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", commandName, err)
		return 1
	}

	if !*execute {
		fmt.Fprintln(stdout, "dry run: no network requests sent; receipts were read only for local validation and session state was not modified")
		fmt.Fprintf(stdout, "purpose: %q\n", plan.Purpose)
		fmt.Fprintf(stdout, "receipts: %d\n", len(plan.Receipts))
		for index, planned := range plan.Receipts {
			fmt.Fprintf(stdout, "- receipt %d: %q (%s, %d bytes)\n", index+1, planned.Receipt.Filename, planned.Receipt.MediaType, planned.Receipt.Size)
		}
		fmt.Fprintf(stdout, "requests: %d\n", plan.RequestCount)
		for _, action := range plan.Actions {
			fmt.Fprintf(stdout, "- %s\n", action)
		}
		if *submit {
			fmt.Fprintln(stdout, "rerun with --execute to create the report, attach all receipts, and submit it")
		} else {
			fmt.Fprintln(stdout, "rerun with --execute to create the Draft, attach all receipts, and save and close it")
		}
		return 0
	}

	var execution *namedSessionExecution
	if *sessionName != "" {
		execution, err = beginNamedSessionExecution(*sessionName)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", commandName, err)
			return 1
		}
		client = execution.client
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	result, operationErr := client.CreateReportWithReceipts(ctx, request)
	var checkpointErr error
	if execution != nil {
		checkpointErr = execution.finish(operationErr)
	}
	if operationErr != nil {
		fmt.Fprintf(stderr, "%s: %v\n", commandName, operationErr)
		fmt.Fprintln(stderr, "the report may contain earlier receipts and its final state may be uncertain; do not retry with the same session or HAR")
		if checkpointErr != nil {
			fmt.Fprintf(stderr, "%s: session checkpoint also failed: %v\n", commandName, checkpointErr)
		}
		return 1
	}
	if result.Submitted {
		fmt.Fprintf(stdout, "created and submitted report %s: purpose=%q status=%s receipts=%d receipt-count=%d->%d submitted=true\n",
			result.ReportNumber, result.Purpose, result.Status, len(result.Receipts),
			result.ReceiptCountBefore, result.ReceiptCountAfter)
	} else {
		fmt.Fprintf(stdout, "created draft %s: purpose=%q status=%s receipts=%d receipt-count=%d->%d saved-and-closed=%t\n",
			result.ReportNumber, result.Purpose, result.Status, len(result.Receipts),
			result.ReceiptCountBefore, result.ReceiptCountAfter, result.SavedAndClosed)
	}
	for index, attached := range result.Receipts {
		fmt.Fprintf(stdout, "- attached %d: %q (%d bytes), cumulative-receipt-count=%d\n",
			index+1, attached.Attached.Filename, attached.Attached.Size, attached.ReceiptCountAfter)
	}
	if checkpointErr != nil {
		fmt.Fprintf(stderr, "%s: report operation succeeded, but session checkpoint failed; do not retry this expense operation: %v\n", commandName, checkpointErr)
		return 1
	}
	return 0
}

func receiptInputsFromPaths(paths []string, notes string, maxSize int64) ([]expense.CreateReportReceiptInput, error) {
	result := make([]expense.CreateReportReceiptInput, 0, len(paths))
	for index, path := range paths {
		receipt, err := receiptInputFromPath(path, maxSize)
		if err != nil {
			return nil, fmt.Errorf("validate receipt %d: %w", index+1, err)
		}
		result = append(result, expense.CreateReportReceiptInput{Notes: notes, Receipt: receipt})
	}
	return result, nil
}
