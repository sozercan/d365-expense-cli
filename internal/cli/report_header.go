package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sozercan/d365-expense-cli/internal/expense"
)

// approverList is a repeatable flag of approver display names. A nil list
// means the flag was not given.
type approverList []string

func (names *approverList) String() string { return strings.Join(*names, ", ") }
func (names *approverList) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("approver name must not be empty")
	}
	*names = append(*names, value)
	return nil
}

// approverFlags are shared by the commands that set approvers.
type approverFlags struct {
	interim approverList
	final   *string
}

func newApproverFlags(flags *flag.FlagSet) *approverFlags {
	approvers := &approverFlags{}
	flags.Var(&approvers.interim, "interim-approver", "interim approver display name or user alias; repeat in approval order to replace the list")
	approvers.final = flags.String("final-approver", "", "final approver display name")
	return approvers
}

// list returns the requested interim approvers, or nil when none were given.
func (approvers *approverFlags) list() []string {
	if len(approvers.interim) == 0 {
		return nil
	}
	return append([]string(nil), approvers.interim...)
}

func printReportHeader(stdout io.Writer, header *expense.ReportHeader) {
	if header == nil {
		return
	}
	fmt.Fprintf(stdout, "interim approvers: %s\n", strings.Join(header.InterimApprovers, ", "))
	fmt.Fprintf(stdout, "final approver: %s\n", header.FinalApprover)
}

func runEditReport(args []string, stdout, stderr io.Writer) int {
	const name = "report edit"
	report := newReportFlags(name, stderr, 3*time.Minute)
	title := report.flags.String("title", "", "new title/purpose")
	approvers := newApproverFlags(report.flags)
	client, code := report.parse(name, args, stderr)
	if client == nil {
		return code
	}
	input := expense.ReportHeaderInput{Title: *title, InterimApprovers: approvers.list(), FinalApprover: *approvers.final}
	if input.Title == "" && input.InterimApprovers == nil && input.FinalApprover == "" {
		fmt.Fprintf(stderr, "%s requires --title, --interim-approver, or --final-approver\n", name)
		return 2
	}
	plan, err := expense.PlanReportHeader(input)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	if !*report.execute {
		printReportPlan(stdout, *report.reportNumber, append(append([]string{"open the Draft"}, plan...), "save and close the Draft"))
		return 0
	}
	return report.run(name, client, stderr, func(ctx context.Context, client *expense.Client) error {
		header, err := client.EditDraftReportHeader(ctx, *report.reportNumber, input)
		if err == nil {
			fmt.Fprintf(stdout, "updated draft %s: title=%q\n", *report.reportNumber, header.Title)
			printReportHeader(stdout, &header)
		}
		return err
	})
}
