package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/kong"
)

var version = "dev"

// defaultDeleteTimeout allows for Dynamics taking close to a minute to delete
// a report with lines.
const defaultDeleteTimeout = 3 * time.Minute

// expenseLineTimeout is the default time budget added per expense line.
const expenseLineTimeout = 90 * time.Second

type canonicalCLI struct {
	ConfigDir string           `name:"config-dir" type:"path" env:"D365_EXPENSE_CONFIG_DIR" help:"Configuration directory."`
	Timeout   time.Duration    `name:"timeout" default:"0s" help:"Overall operation timeout; zero uses the command default."`
	Verbose   bool             `short:"v" xor:"verbosity" help:"Enable diagnostic output."`
	Quiet     bool             `short:"q" xor:"verbosity" help:"Suppress normal output."`
	NoColor   bool             `name:"no-color" env:"NO_COLOR" help:"Disable colored output."`
	Version   kong.VersionFlag `name:"version" help:"Print version and exit."`

	Create     createCommand  `cmd:"" help:"Create and submit an expense report; use --draft to save without submitting."`
	Receipt    receiptCommand `cmd:"" help:"Manage receipts on existing Draft reports."`
	Report     reportCommand  `cmd:"" help:"Manage existing Draft reports."`
	Expense    expenseCommand `cmd:"" help:"Manage unattached expenses."`
	Session    sessionCommand `cmd:"" help:"Manage imported Dynamics sessions."`
	HAR        harCommand     `cmd:"" name:"har" help:"Inspect or capture private HAR files."`
	VersionCmd versionCommand `cmd:"" name:"version" help:"Print version information."`
}

type profileSource struct {
	HAR     string `name:"har" type:"existingfile" help:"Private raw workspace HAR."`
	Session string `name:"session" help:"Named imported session."`
}

func (source profileSource) validate() error {
	if (source.HAR == "") == (source.Session == "") {
		return errors.New("exactly one of --har or --session is required")
	}
	return nil
}

type createCommand struct {
	profileSource      `embed:""`
	Draft              bool     `name:"draft" help:"Save and close as a Draft instead of submitting."`
	Purpose            string   `name:"purpose" required:"" help:"Expense report purpose/title."`
	Expenses           string   `name:"expenses" type:"existingfile" placeholder:"FILE" help:"JSON file of expense lines, optionally itemized, to add; requires --draft."`
	Receipts           []string `name:"receipt" type:"existingfile" placeholder:"FILE" help:"PNG receipt; repeat in attachment order."`
	ReceiptNote        string   `name:"receipt-note" help:"Notes applied to every receipt."`
	InterimApprovers   []string `name:"interim-approver" placeholder:"NAME" help:"Interim approver display name or user alias; repeat in approval order to replace the default list."`
	FinalApprover      string   `name:"final-approver" placeholder:"NAME" help:"Final approver display name, replacing the default."`
	ReceiptProtocolHAR string   `name:"receipt-protocol-har" type:"existingfile" hidden:""`
	DryRun             bool     `name:"dry-run" help:"Validate and print the plan without network requests."`
}

func (command *createCommand) Validate() error {
	if err := command.profileSource.validate(); err != nil {
		return err
	}
	if len(command.Receipts) > maxCLIReceipts {
		return fmt.Errorf("at most %d receipts are supported", maxCLIReceipts)
	}
	if command.ReceiptNote != "" && len(command.Receipts) == 0 {
		return errors.New("--receipt-note requires at least one --receipt")
	}
	if command.ReceiptProtocolHAR != "" && len(command.Receipts) == 0 {
		return errors.New("--receipt-protocol-har requires at least one --receipt")
	}
	if command.Expenses != "" && !command.Draft {
		return errors.New("--expenses currently requires --draft; review and submit the report in Dynamics")
	}
	return nil
}

type receiptCommand struct {
	Attach receiptAttachCommand `cmd:"" help:"Attach a receipt to an existing captured Draft."`
	Delete receiptDeleteCommand `cmd:"" help:"Delete unattached receipts with one file name."`
}

type receiptDeleteCommand struct {
	profileSource `embed:""`
	Name          string `name:"name" required:"" help:"Exact receipt file name."`
	Count         int    `name:"count" required:"" help:"Number of unattached receipts with that name; must match exactly."`
	DryRun        bool   `name:"dry-run" help:"Validate and print the plan without network requests."`
}

type expenseCommand struct {
	List   expenseListCommand   `cmd:"" help:"List unattached expenses."`
	Delete expenseDeleteCommand `cmd:"" help:"Delete unattached expenses from one merchant."`
}

type expenseListCommand struct {
	profileSource `embed:""`
}

type expenseDeleteCommand struct {
	profileSource `embed:""`
	Merchant      string `name:"merchant" required:"" help:"Exact merchant of the unattached expenses."`
	Count         int    `name:"count" required:"" help:"Number of unattached expenses from that merchant; must match exactly."`
	DryRun        bool   `name:"dry-run" help:"Validate and print the plan without network requests."`
}

type receiptAttachCommand struct {
	Draft       bool   `name:"draft" required:"" help:"Required safety acknowledgement: modify only a Draft report."`
	HAR         string `name:"har" required:"" type:"existingfile" help:"Private receipt-capable HAR."`
	Report      string `name:"report" required:"" help:"Draft expense report number."`
	Receipt     string `name:"receipt" required:"" type:"existingfile" help:"Private PNG receipt."`
	ReceiptNote string `name:"receipt-note" help:"Receipt notes."`
	DryRun      bool   `name:"dry-run" help:"Validate without network requests."`
}

func (command *receiptAttachCommand) Validate() error {
	if !command.Draft {
		return errors.New("--draft is required")
	}
	return nil
}

type reportCommand struct {
	Show          reportShowCommand          `cmd:"" help:"List the expense lines of an existing Draft report."`
	Itemize       reportItemizeCommand       `cmd:"" help:"Itemize one expense line of an existing Draft report."`
	Submit        reportSubmitCommand        `cmd:"" help:"Submit an existing Draft report."`
	Edit          reportEditCommand          `cmd:"" help:"Change an existing Draft report's title or approvers."`
	AddReceipt    reportAddReceiptCommand    `cmd:"" name:"add-receipt" help:"Upload PNG receipts to an existing Draft report."`
	AttachReceipt reportAttachReceiptCommand `cmd:"" name:"attach-receipt" help:"Attach a receipt already on a Draft report to one of its lines."`
	RemoveReceipt reportRemoveReceiptCommand `cmd:"" name:"remove-receipt" help:"Remove receipts with one file name from a Draft report."`
	Delete        reportDeleteCommand        `cmd:"" help:"Delete an existing Draft report."`
}

type reportShowCommand struct {
	profileSource `embed:""`
	Report        string `name:"report" required:"" help:"Draft expense report number."`
}

type reportItemizeCommand struct {
	profileSource `embed:""`
	Draft         bool   `name:"draft" required:"" help:"Required safety acknowledgement: modify only a Draft report."`
	Report        string `name:"report" required:"" help:"Draft expense report number."`
	Line          int    `name:"line" required:"" help:"Line number shown by report show."`
	Itemization   string `name:"itemization" required:"" type:"existingfile" placeholder:"FILE" help:"JSON file of itemization rows."`
	DryRun        bool   `name:"dry-run" help:"Validate and print the plan without network requests."`
}

type reportEditCommand struct {
	profileSource    `embed:""`
	Draft            bool     `name:"draft" required:"" help:"Required safety acknowledgement: modify only a Draft report."`
	Report           string   `name:"report" required:"" help:"Draft expense report number."`
	Title            string   `name:"title" help:"New title/purpose."`
	InterimApprovers []string `name:"interim-approver" placeholder:"NAME" help:"Interim approver display name or user alias; repeat in approval order to replace the list."`
	FinalApprover    string   `name:"final-approver" placeholder:"NAME" help:"Final approver display name."`
	DryRun           bool     `name:"dry-run" help:"Validate and print the plan without network requests."`
}

type reportAddReceiptCommand struct {
	profileSource `embed:""`
	Draft         bool     `name:"draft" required:"" help:"Required safety acknowledgement: modify only a Draft report."`
	Report        string   `name:"report" required:"" help:"Draft expense report number."`
	Receipts      []string `name:"receipt" required:"" type:"existingfile" placeholder:"FILE" help:"PNG receipt; repeat in upload order. Dynamics lists it by file name without the extension."`
	ReceiptNote   string   `name:"receipt-note" help:"Notes applied to every receipt."`
	DryRun        bool     `name:"dry-run" help:"Validate and print the plan without network requests."`
}

type reportAttachReceiptCommand struct {
	profileSource `embed:""`
	Draft         bool   `name:"draft" required:"" help:"Required safety acknowledgement: modify only a Draft report."`
	Report        string `name:"report" required:"" help:"Draft expense report number."`
	Line          int    `name:"line" required:"" help:"Line number shown by report show."`
	Receipt       string `name:"receipt" required:"" help:"Receipt file name as listed on the report's Receipts tab."`
	DryRun        bool   `name:"dry-run" help:"Validate and print the plan without network requests."`
}

type reportRemoveReceiptCommand struct {
	profileSource `embed:""`
	Draft         bool   `name:"draft" required:"" help:"Required safety acknowledgement: modify only a Draft report."`
	Report        string `name:"report" required:"" help:"Draft expense report number."`
	Receipt       string `name:"receipt" required:"" help:"Receipt file name as listed on the report's Receipts tab."`
	Count         int    `name:"count" default:"1" help:"Number of receipts with that name; must match exactly."`
	DryRun        bool   `name:"dry-run" help:"Validate and print the plan without network requests."`
}

type reportSubmitCommand struct {
	profileSource `embed:""`
	Report        string `name:"report" required:"" help:"Draft expense report number."`
	DryRun        bool   `name:"dry-run" help:"Validate and print the plan without network requests."`
}

type reportDeleteCommand struct {
	profileSource `embed:""`
	Draft         bool   `name:"draft" required:"" help:"Required safety acknowledgement: delete only a Draft report."`
	Report        string `name:"report" required:"" help:"Draft expense report number."`
	DryRun        bool   `name:"dry-run" help:"Validate and print the plan without network requests."`
}

func (command *reportDeleteCommand) Validate() error {
	if !command.Draft {
		return errors.New("--draft is required")
	}
	return command.profileSource.validate()
}

type sessionCommand struct {
	Import     sessionImportCommand     `cmd:"" help:"Import a named session from a HAR or local CDP."`
	List       sessionListCommand       `cmd:"" help:"List imported sessions."`
	Show       sessionShowCommand       `cmd:"" help:"Show safe session metadata."`
	Remove     sessionRemoveCommand     `cmd:"" help:"Remove a named session."`
	Unlock     sessionUnlockCommand     `cmd:"" help:"Remove a stale session lock."`
	CleanupKey sessionCleanupKeyCommand `cmd:"" name:"cleanup-key" help:"Remove an orphaned session encryption key after a reported partial cleanup failure."`
}

type sessionImportCommand struct {
	Name         string        `arg:"" name:"name" help:"Session name."`
	HAR          string        `name:"har" type:"existingfile" help:"Import from a private raw workspace HAR."`
	CDP          string        `name:"cdp" placeholder:"ENDPOINT" help:"Import from an authenticated local CDP browser."`
	WorkspaceURL string        `name:"url" help:"Optional Dynamics Expense workspace URL; auto-detected when omitted."`
	Wait         time.Duration `default:"20s" help:"CDP workspace recording time."`
	Force        bool          `help:"Replace an existing session."`
}

func (command *sessionImportCommand) Validate() error {
	if (command.HAR == "") == (command.CDP == "") {
		return errors.New("exactly one of --har or --cdp is required")
	}
	if command.CDP == "" && command.WorkspaceURL != "" {
		return errors.New("workspace URL is only valid with --cdp")
	}
	return nil
}

type sessionListCommand struct{}
type sessionShowCommand struct {
	Name string `arg:"" name:"name" help:"Session name."`
}
type sessionRemoveCommand struct {
	Name string `arg:"" name:"name" help:"Session name."`
}
type sessionUnlockCommand struct {
	Name string `arg:"" name:"name" help:"Session name."`
}
type sessionCleanupKeyCommand struct {
	ID string `arg:"" name:"key-id" help:"Encryption key ID reported by a partial cleanup error."`
}

type harCommand struct {
	Inspect harInspectCommand `cmd:"" help:"Inspect an executable private HAR."`
	Capture harCaptureCommand `cmd:"" help:"Capture an Expense workspace bootstrap."`
}

type harInspectCommand struct {
	Path string `arg:"" name:"har" type:"existingfile" help:"Private HAR file."`
}

type harCaptureCommand struct {
	WorkspaceURL   string        `arg:"" name:"workspace-url" help:"Dynamics Expense workspace URL."`
	Out            string        `name:"out" required:"" type:"path" help:"Private HAR output path."`
	CDP            string        `name:"cdp" default:"http://127.0.0.1:9222" help:"CDP endpoint."`
	Wait           time.Duration `default:"10s" help:"Post-load capture time."`
	Force          bool          `help:"Replace an existing regular file."`
	AllowRemoteCDP bool          `name:"allow-remote-cdp" help:"Allow an exact remote browser WebSocket endpoint."`
}

type versionCommand struct{}

type commandRuntime struct {
	stdout  io.Writer
	stderr  io.Writer
	global  *canonicalCLI
	runners legacyRunners
}

type legacyRunners struct {
	createReport             func([]string, io.Writer, io.Writer) int
	createReportWithReceipts func([]string, io.Writer, io.Writer) int
	attachReceipt            func([]string, io.Writer, io.Writer) int
	deleteReport             func([]string, io.Writer, io.Writer) int
	showReport               func([]string, io.Writer, io.Writer) int
	itemizeReportLine        func([]string, io.Writer, io.Writer) int
	submitReport             func([]string, io.Writer, io.Writer) int
	editReport               func([]string, io.Writer, io.Writer) int
	addReportReceipts        func([]string, io.Writer, io.Writer) int
	attachReportReceipt      func([]string, io.Writer, io.Writer) int
	removeReportReceipts     func([]string, io.Writer, io.Writer) int
	listExpenses             func([]string, io.Writer, io.Writer) int
	deleteExpenses           func([]string, io.Writer, io.Writer) int
	deleteReceipts           func([]string, io.Writer, io.Writer) int
	sessionImport            func([]string, io.Writer, io.Writer) int
	sessionImportCDP         func([]string, io.Writer, io.Writer) int
	sessionList              func([]string, io.Writer, io.Writer) int
	sessionInspect           func([]string, io.Writer, io.Writer) int
	sessionRemove            func([]string, io.Writer, io.Writer) int
	sessionUnlock            func([]string, io.Writer, io.Writer) int
	sessionCleanupKey        func([]string, io.Writer, io.Writer) int
	harInspect               func([]string, io.Writer, io.Writer) int
	harCapture               func([]string, io.Writer, io.Writer) int
}

func defaultLegacyRunners() legacyRunners {
	return legacyRunners{
		createReport:             runCreateDraft,
		createReportWithReceipts: runCreateDraftWithReceipts,
		attachReceipt:            runAttachReceipt,
		deleteReport:             runDeleteReport,
		showReport:               runShowReport,
		itemizeReportLine:        runItemizeReportLine,
		submitReport:             runSubmitReport,
		editReport:               runEditReport,
		addReportReceipts:        runAddReportReceipts,
		attachReportReceipt:      runAttachReportReceipt,
		removeReportReceipts:     runRemoveReportReceipts,
		listExpenses:             runListExpenses,
		deleteExpenses:           runDeleteExpenses,
		deleteReceipts:           runDeleteReceipts,
		sessionImport:            runSessionImport,
		sessionImportCDP:         runSessionImportCDP,
		sessionList:              runSessionList,
		sessionInspect:           runSessionInspect,
		sessionRemove:            runSessionRemove,
		sessionUnlock:            runSessionUnlock,
		sessionCleanupKey:        runSessionCleanupKey,
		harInspect:               runInspect,
		harCapture:               runCaptureDraft,
	}
}

type exitError struct{ code int }

func (e *exitError) Error() string { return "command failed" }
func (e *exitError) ExitCode() int { return e.code }

func invokeLegacy(run func([]string, io.Writer, io.Writer) int, args []string, rt *commandRuntime) error {
	code := run(args, rt.stdout, rt.stderr)
	if code == 0 {
		return nil
	}
	return &exitError{code: code}
}

func sourceArgs(source profileSource) []string {
	if source.HAR != "" {
		return []string{"--har", source.HAR}
	}
	return []string{"--session", source.Session}
}

func selectedTimeout(global time.Duration, fallback time.Duration) time.Duration {
	if global > 0 {
		return global
	}
	return fallback
}

func (command *createCommand) Run(rt *commandRuntime) error {
	if err := command.Validate(); err != nil {
		return err
	}
	args := sourceArgs(command.profileSource)
	args = append(args, "--purpose", command.Purpose)
	if !command.Draft {
		args = append(args, "--submit")
	}
	args = append(args, approverArgs(command.InterimApprovers, command.FinalApprover)...)
	// Every expense line adds several sequential round trips, so the default
	// budget grows with the file; parse errors are reported by the runner.
	var expenseBudget time.Duration
	if command.Expenses != "" {
		args = append(args, "--expenses", command.Expenses)
		if lines, err := loadExpenseFile(command.Expenses); err == nil {
			expenseBudget = time.Duration(len(lines)) * expenseLineTimeout
		}
	}
	if len(command.InterimApprovers) != 0 || command.FinalApprover != "" {
		expenseBudget += time.Minute
	}
	if len(command.Receipts) == 0 {
		args = append(args, "--timeout", selectedTimeout(rt.global.Timeout, 45*time.Second+expenseBudget).String())
		if !command.DryRun {
			args = append(args, "--execute")
		}
		return invokeLegacy(rt.runners.createReport, args, rt)
	}
	args = append(args, "--timeout", selectedTimeout(rt.global.Timeout, 120*time.Second+expenseBudget).String())
	for _, receipt := range command.Receipts {
		args = append(args, "--file", receipt)
	}
	if command.ReceiptNote != "" {
		args = append(args, "--notes", command.ReceiptNote)
	}
	if command.ReceiptProtocolHAR != "" {
		args = append(args, "--receipt-protocol-har", command.ReceiptProtocolHAR)
	}
	if !command.DryRun {
		args = append(args, "--execute")
	}
	return invokeLegacy(rt.runners.createReportWithReceipts, args, rt)
}

func (command *receiptAttachCommand) Run(rt *commandRuntime) error {
	if err := command.Validate(); err != nil {
		return err
	}
	args := []string{"--har", command.HAR, "--report", command.Report, "--file", command.Receipt,
		"--timeout", selectedTimeout(rt.global.Timeout, 90*time.Second).String()}
	if command.ReceiptNote != "" {
		args = append(args, "--notes", command.ReceiptNote)
	}
	if !command.DryRun {
		args = append(args, "--execute")
	}
	return invokeLegacy(rt.runners.attachReceipt, args, rt)
}

func (command *reportDeleteCommand) Run(rt *commandRuntime) error {
	if err := command.Validate(); err != nil {
		return err
	}
	args := append(sourceArgs(command.profileSource), "--report", command.Report,
		"--timeout", selectedTimeout(rt.global.Timeout, defaultDeleteTimeout).String())
	if !command.DryRun {
		args = append(args, "--execute")
	}
	return invokeLegacy(rt.runners.deleteReport, args, rt)
}

func reportArgs(source profileSource, report string, timeout time.Duration, fallback time.Duration, dryRun bool) []string {
	args := append(sourceArgs(source), "--report", report, "--timeout", selectedTimeout(timeout, fallback).String())
	if !dryRun {
		args = append(args, "--execute")
	}
	return args
}

func (command *reportShowCommand) Run(rt *commandRuntime) error {
	if err := command.profileSource.validate(); err != nil {
		return err
	}
	return invokeLegacy(rt.runners.showReport, reportArgs(command.profileSource, command.Report, rt.global.Timeout, 2*time.Minute, false), rt)
}

func (command *reportItemizeCommand) Run(rt *commandRuntime) error {
	if !command.Draft {
		return errors.New("--draft is required")
	}
	if err := command.profileSource.validate(); err != nil {
		return err
	}
	args := reportArgs(command.profileSource, command.Report, rt.global.Timeout, 5*time.Minute, command.DryRun)
	args = append(args, "--line", strconv.Itoa(command.Line), "--itemization", command.Itemization)
	return invokeLegacy(rt.runners.itemizeReportLine, args, rt)
}

func (command *reportEditCommand) Run(rt *commandRuntime) error {
	if !command.Draft {
		return errors.New("--draft is required")
	}
	if err := command.profileSource.validate(); err != nil {
		return err
	}
	if command.Title == "" && len(command.InterimApprovers) == 0 && command.FinalApprover == "" {
		return errors.New("--title, --interim-approver, or --final-approver is required")
	}
	args := reportArgs(command.profileSource, command.Report, rt.global.Timeout, 3*time.Minute, command.DryRun)
	if command.Title != "" {
		args = append(args, "--title", command.Title)
	}
	args = append(args, approverArgs(command.InterimApprovers, command.FinalApprover)...)
	return invokeLegacy(rt.runners.editReport, args, rt)
}

func approverArgs(interim []string, final string) []string {
	var args []string
	for _, name := range interim {
		args = append(args, "--interim-approver", name)
	}
	if final != "" {
		args = append(args, "--final-approver", final)
	}
	return args
}

func (command *reportAddReceiptCommand) Run(rt *commandRuntime) error {
	if !command.Draft {
		return errors.New("--draft is required")
	}
	if err := command.profileSource.validate(); err != nil {
		return err
	}
	if len(command.Receipts) > maxCLIReceipts {
		return fmt.Errorf("at most %d receipts are supported", maxCLIReceipts)
	}
	timeout := 2*time.Minute + time.Duration(len(command.Receipts))*time.Minute
	args := reportArgs(command.profileSource, command.Report, rt.global.Timeout, timeout, command.DryRun)
	for _, receipt := range command.Receipts {
		args = append(args, "--file", receipt)
	}
	if command.ReceiptNote != "" {
		args = append(args, "--notes", command.ReceiptNote)
	}
	return invokeLegacy(rt.runners.addReportReceipts, args, rt)
}

func (command *reportAttachReceiptCommand) Run(rt *commandRuntime) error {
	if !command.Draft {
		return errors.New("--draft is required")
	}
	if err := command.profileSource.validate(); err != nil {
		return err
	}
	args := reportArgs(command.profileSource, command.Report, rt.global.Timeout, 3*time.Minute, command.DryRun)
	args = append(args, "--line", strconv.Itoa(command.Line), "--receipt", command.Receipt)
	return invokeLegacy(rt.runners.attachReportReceipt, args, rt)
}

func (command *reportRemoveReceiptCommand) Run(rt *commandRuntime) error {
	if !command.Draft {
		return errors.New("--draft is required")
	}
	if err := command.profileSource.validate(); err != nil {
		return err
	}
	args := reportArgs(command.profileSource, command.Report, rt.global.Timeout, 5*time.Minute, command.DryRun)
	args = append(args, "--receipt", command.Receipt, "--count", strconv.Itoa(command.Count))
	return invokeLegacy(rt.runners.removeReportReceipts, args, rt)
}

func workspaceArgs(source profileSource, timeout time.Duration, dryRun bool, extra ...string) []string {
	args := append(sourceArgs(source), "--timeout", selectedTimeout(timeout, 5*time.Minute).String())
	if !dryRun {
		args = append(args, "--execute")
	}
	return append(args, extra...)
}

func (command *expenseListCommand) Run(rt *commandRuntime) error {
	if err := command.profileSource.validate(); err != nil {
		return err
	}
	return invokeLegacy(rt.runners.listExpenses, workspaceArgs(command.profileSource, rt.global.Timeout, false), rt)
}

func (command *expenseDeleteCommand) Run(rt *commandRuntime) error {
	if err := command.profileSource.validate(); err != nil {
		return err
	}
	return invokeLegacy(rt.runners.deleteExpenses, workspaceArgs(command.profileSource, rt.global.Timeout, command.DryRun,
		"--merchant", command.Merchant, "--count", strconv.Itoa(command.Count)), rt)
}

func (command *receiptDeleteCommand) Run(rt *commandRuntime) error {
	if err := command.profileSource.validate(); err != nil {
		return err
	}
	return invokeLegacy(rt.runners.deleteReceipts, workspaceArgs(command.profileSource, rt.global.Timeout, command.DryRun,
		"--name", command.Name, "--count", strconv.Itoa(command.Count)), rt)
}

func (command *reportSubmitCommand) Run(rt *commandRuntime) error {
	if err := command.profileSource.validate(); err != nil {
		return err
	}
	return invokeLegacy(rt.runners.submitReport, reportArgs(command.profileSource, command.Report, rt.global.Timeout, 3*time.Minute, command.DryRun), rt)
}

func (command *sessionImportCommand) Run(rt *commandRuntime) error {
	if err := command.Validate(); err != nil {
		return err
	}
	if command.HAR != "" {
		args := []string{"--name", command.Name}
		if command.Force {
			args = append(args, "--force")
		}
		args = append(args, command.HAR)
		return invokeLegacy(rt.runners.sessionImport, args, rt)
	}
	workspaceURL := command.WorkspaceURL
	if workspaceURL == "" {
		_, discoveredURL, err := discoverExpenseTarget(command.CDP, "")
		if err != nil {
			return err
		}
		workspaceURL = discoveredURL
	}
	args := []string{"--name", command.Name, "--cdp", command.CDP, "--wait", command.Wait.String(),
		"--timeout", selectedTimeout(rt.global.Timeout, 60*time.Second).String()}
	if command.Force {
		args = append(args, "--force")
	}
	args = append(args, workspaceURL)
	return invokeLegacy(rt.runners.sessionImportCDP, args, rt)
}

func (command *sessionListCommand) Run(rt *commandRuntime) error {
	return invokeLegacy(rt.runners.sessionList, nil, rt)
}
func (command *sessionShowCommand) Run(rt *commandRuntime) error {
	return invokeLegacy(rt.runners.sessionInspect, []string{"--name", command.Name}, rt)
}
func (command *sessionRemoveCommand) Run(rt *commandRuntime) error {
	return invokeLegacy(rt.runners.sessionRemove, []string{"--name", command.Name}, rt)
}
func (command *sessionUnlockCommand) Run(rt *commandRuntime) error {
	return invokeLegacy(rt.runners.sessionUnlock, []string{"--name", command.Name}, rt)
}
func (command *sessionCleanupKeyCommand) Run(rt *commandRuntime) error {
	return invokeLegacy(rt.runners.sessionCleanupKey, []string{"--id", command.ID}, rt)
}
func (command *harInspectCommand) Run(rt *commandRuntime) error {
	return invokeLegacy(rt.runners.harInspect, []string{"--har", command.Path}, rt)
}
func (command *harCaptureCommand) Run(rt *commandRuntime) error {
	args := []string{"--out", command.Out, "--cdp", command.CDP, "--wait", command.Wait.String(),
		"--timeout", selectedTimeout(rt.global.Timeout, 45*time.Second).String()}
	if command.Force {
		args = append(args, "--force")
	}
	if command.AllowRemoteCDP {
		args = append(args, "--allow-remote-cdp")
	}
	args = append(args, command.WorkspaceURL)
	return invokeLegacy(rt.runners.harCapture, args, rt)
}
func (command *versionCommand) Run(rt *commandRuntime) error {
	fmt.Fprintln(rt.stdout, "d365-expense", version)
	return nil
}

type kongExitSignal struct{ code int }

func runCanonical(args []string, stdout, stderr io.Writer) int {
	return runCanonicalWithRunners(args, stdout, stderr, defaultLegacyRunners())
}

func runCanonicalWithRunners(args []string, stdout, stderr io.Writer, runners legacyRunners) int {
	model := &canonicalCLI{}
	parser, err := kong.New(model,
		kong.Name("d365-expense"),
		kong.Description("Create and submit Dynamics 365 expense reports; use --draft to save without submitting."),
		kong.Vars{"version": version},
		kong.Writers(stdout, stderr),
		kong.Exit(func(code int) { panic(kongExitSignal{code: code}) }),
	)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var ctx *kong.Context
	parseExit := -1
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if signal, ok := recovered.(kongExitSignal); ok {
					parseExit = signal.code
					return
				}
				panic(recovered)
			}
		}()
		ctx, err = parser.Parse(args)
	}()
	if parseExit >= 0 {
		return parseExit
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if model.ConfigDir != "" {
		_ = os.Setenv("D365_EXPENSE_CONFIG_DIR", model.ConfigDir)
	}
	if model.Timeout < 0 {
		fmt.Fprintln(stderr, "--timeout must be non-negative")
		return 2
	}
	rt := &commandRuntime{stdout: stdout, stderr: stderr, global: model, runners: runners}
	if model.Quiet {
		rt.stdout = io.Discard
	}
	if err := ctx.Run(rt); err != nil {
		var exit *exitError
		if errors.As(err, &exit) {
			return exit.code
		}
		fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}

func isLegacyInvocation(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "create-draft", "create-draft-with-receipt", "create-draft-with-receipts", "attach-receipt", "capture-draft", "inspect", "submit":
		return true
	case "session":
		if len(args) < 2 {
			return false
		}
		switch args[1] {
		case "import-cdp", "inspect":
			return true
		case "import", "remove", "unlock":
			for _, arg := range args[2:] {
				if arg == "--name" {
					return true
				}
			}
		}
	}
	return false
}

func legacyReplacement(args []string) string {
	if len(args) == 0 {
		return "d365-expense --help"
	}
	switch args[0] {
	case "create-draft", "create-draft-with-receipt", "create-draft-with-receipts":
		if legacySubmitRequested(args) {
			return "d365-expense create"
		}
		return "d365-expense create --draft"
	case "attach-receipt":
		return "d365-expense receipt attach --draft"
	case "capture-draft":
		return "d365-expense har capture"
	case "inspect":
		return "d365-expense har inspect"
	case "session":
		return "d365-expense session " + args[1]
	default:
		return "d365-expense --help"
	}
}

func legacySubmitRequested(args []string) bool {
	requested := false
	for _, arg := range args[1:] {
		if arg == "--" {
			break
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if name != "--submit" && name != "-submit" {
			continue
		}
		if !hasValue {
			requested = true
			continue
		}
		parsed, err := strconv.ParseBool(value)
		if err == nil {
			requested = parsed
		}
	}
	return requested
}

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		args = []string{"--help"}
	}
	if len(args) == 1 && args[0] == "help" {
		args = []string{"--help"}
	}
	if len(args) == 2 && args[0] == "session" && args[1] == "help" {
		args = []string{"session", "--help"}
	}
	if isLegacyInvocation(args) {
		if len(args) > 0 && args[0] != "submit" {
			fmt.Fprintf(stderr, "warning: legacy command is deprecated; use %s\n", legacyReplacement(args))
		}
		return runLegacy(args, stdout, stderr)
	}
	return runCanonical(args, stdout, stderr)
}

// run is retained for package-level compatibility tests.
func run(args []string, stdout, stderr io.Writer) int { return Run(args, stdout, stderr) }
