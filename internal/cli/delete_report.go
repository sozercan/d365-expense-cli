package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/sozercan/d365-expense-cli/internal/expense"
)

func runDeleteReport(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("report delete", flag.ContinueOnError)
	flags.SetOutput(stderr)
	harPath := flags.String("har", "", "path to a private raw HAR capture")
	sessionName := flags.String("session", "", "named imported standalone session")
	reportNumber := flags.String("report", "", "Draft expense report number")
	execute := flags.Bool("execute", false, "delete the Draft report")
	timeout := flags.Duration("timeout", defaultDeleteTimeout, "overall execution timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "report delete does not accept positional arguments")
		return 2
	}
	if err := validateProfileSource("report delete", *harPath, *sessionName); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *reportNumber == "" || *timeout <= 0 {
		fmt.Fprintln(stderr, "report delete requires --report and a positive --timeout")
		return 2
	}
	profile, err := loadBootstrapForRead(*harPath, *sessionName)
	if err != nil {
		fmt.Fprintf(stderr, "report delete: %v\n", err)
		return 1
	}
	client, err := expense.NewFromBootstrap(profile)
	if err != nil {
		fmt.Fprintf(stderr, "report delete: %v\n", err)
		return 1
	}
	request := expense.DeleteReportRequest{ReportNumber: *reportNumber}
	plan, err := client.PlanDeleteDraftReport(request)
	if err != nil {
		fmt.Fprintf(stderr, "report delete: %v\n", err)
		return 1
	}
	if !*execute {
		fmt.Fprintln(stdout, "dry run: no network requests sent; imported session state was not modified")
		fmt.Fprintf(stdout, "report: %s\n", plan.ReportNumber)
		fmt.Fprintf(stdout, "requests: %d\n", plan.RequestCount)
		for _, action := range plan.Actions {
			fmt.Fprintf(stdout, "- %s\n", action)
		}
		return 0
	}

	var execution *namedSessionExecution
	if *sessionName != "" {
		execution, err = beginNamedSessionExecution(*sessionName)
		if err != nil {
			fmt.Fprintf(stderr, "report delete: %v\n", err)
			return 1
		}
		client = execution.client
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	result, operationErr := client.DeleteDraftReport(ctx, request)
	var checkpointErr error
	if execution != nil {
		checkpointErr = execution.finish(operationErr)
	}
	if operationErr != nil {
		fmt.Fprintf(stderr, "report delete: %v\n", operationErr)
		if errors.Is(operationErr, expense.ErrOperationUncertain) {
			fmt.Fprintln(stderr, "the report may already have been deleted; verify Dynamics and do not retry with the same session or HAR")
		}
		if checkpointErr != nil {
			fmt.Fprintf(stderr, "report delete: session checkpoint also failed: %v\n", checkpointErr)
		}
		return 1
	}
	fmt.Fprintf(stdout, "deleted draft %s: purpose=%q\n", result.ReportNumber, result.Purpose)
	if checkpointErr != nil {
		fmt.Fprintf(stderr, "report delete: report was deleted, but session checkpoint failed; re-import before the next operation: %v\n", checkpointErr)
		return 1
	}
	return 0
}
