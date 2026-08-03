package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/expense"
)

func TestHelpAndSubmitMode(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"create", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit code = %d", code)
	}
	help := strings.ToLower(strings.ReplaceAll(stdout.String(), "\n", " "))
	if !strings.Contains(help, "submit") || !strings.Contains(help, "--draft") {
		t.Fatalf("help did not describe default submission and the Draft opt-out: %s", stdout.String())
	}
	if strings.Contains(help, "--submit") || strings.Contains(help, "--confirm-submit") {
		t.Fatalf("help exposed redundant submission flags: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"submit"}, &stdout, &stderr); code != 2 {
		t.Fatalf("submit exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "d365-expense create") {
		t.Fatalf("submit rejection = %s", stderr.String())
	}
}

func TestCreateDraftRequiresExplicitInputs(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"create-draft"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "requires exactly one of --har or --session") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestCreateReportFailureWarnsUncertainUsersNotToRetry(t *testing.T) {
	t.Parallel()

	uncertain := func(cause error) error {
		return errors.Join(expense.ErrOperationUncertain, cause)
	}
	tests := []struct {
		name         string
		harPath      string
		submit       bool
		operationErr error
		wantOutcome  string
		wantWarning  string
	}{
		{
			name: "direct HAR uncertain submit", harPath: "capture.har", submit: true,
			operationErr: uncertain(errors.New("response verification failed")),
			wantOutcome:  "created or submitted",
			wantWarning:  "do not retry with the same HAR",
		},
		{
			name: "named session late authentication failure", submit: true,
			operationErr: uncertain(expense.ErrAuthenticationExpired),
			wantOutcome:  "created or submitted",
			wantWarning:  "do not re-import and retry this expense operation",
		},
		{
			name: "named session early authentication failure", submit: true,
			operationErr: expense.ErrAuthenticationExpired,
		},
		{
			name: "direct HAR draft uncertainty", harPath: "capture.har",
			operationErr: uncertain(errors.New("save response failed")),
			wantOutcome:  "created or saved",
			wantWarning:  "do not retry with the same HAR",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			writeCreateReportOperationFailure(&stderr, test.harPath, test.submit, test.operationErr, nil)

			output := stderr.String()
			if test.wantWarning == "" {
				if strings.Contains(output, "may already have been") {
					t.Fatalf("unexpected uncertainty warning: %q", output)
				}
				return
			}
			if !strings.Contains(output, "may already have been "+test.wantOutcome) || !strings.Contains(output, test.wantWarning) {
				t.Fatalf("stderr = %q, want warning %q", output, test.wantWarning)
			}
		})
	}
}

func TestRequirePrivateCapture(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "capture.har")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requirePrivateCapture(path); err != nil {
		t.Fatalf("0600 capture rejected: %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := requirePrivateCapture(path); err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("0644 capture error = %v", err)
	}
}

func TestAttachReceiptRequiresExplicitInputs(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"attach-receipt"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "requires --har, --report, and --file") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestLegacyCreateDraftWithReceiptRequiresExplicitInputs(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"create-draft-with-receipt"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "requires exactly one of --har or --session") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestCreateCommandsRequireExactlyOneProfileSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "draft both", args: []string{"create-draft", "--har", "a.har", "--session", "work", "--purpose", "test"}},
		{name: "draft receipt both", args: []string{"create-draft-with-receipt", "--har", "a.har", "--session", "work", "--purpose", "test", "--file", "receipt.png"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), "exactly one of --har or --session") {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestSessionCommandsRequireExplicitNames(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{"session", "import"},
		{"session", "inspect"},
		{"session", "remove"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("run(%v) code = %d, want 2", args, code)
		}
		if strings.TrimSpace(stderr.String()) == "" {
			t.Fatalf("run(%v) stderr = %q", args, stderr.String())
		}
	}
}
