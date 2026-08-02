package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
