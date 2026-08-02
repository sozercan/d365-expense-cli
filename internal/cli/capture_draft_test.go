package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/har"
)

func TestCaptureDraftValidatesArgumentsBeforeConnecting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing URL", args: []string{"capture-draft", "--out", "capture.har"}, want: "requires exactly one"},
		{name: "missing output", args: []string{"capture-draft", "https://tenant.operations.dynamics.com/"}, want: "requires --out"},
		{name: "stdout disabled", args: []string{"capture-draft", "--out", "-", "https://tenant.operations.dynamics.com/"}, want: "stdout is disabled"},
		{name: "negative wait", args: []string{"capture-draft", "--out", "capture.har", "--wait", "-1s", "https://tenant.operations.dynamics.com/"}, want: "non-negative --wait"},
		{name: "non-positive timeout", args: []string{"capture-draft", "--out", "capture.har", "--timeout", "0s", "https://tenant.operations.dynamics.com/"}, want: "positive --timeout"},
		{name: "wait not shorter", args: []string{"capture-draft", "--out", "capture.har", "--wait", "2s", "--timeout", "2s", "https://tenant.operations.dynamics.com/"}, want: "shorter than --timeout"},
		{name: "http target", args: []string{"capture-draft", "--out", "capture.har", "http://tenant.operations.dynamics.com/"}, want: "must use https"},
		{name: "foreign target", args: []string{"capture-draft", "--out", "capture.har", "https://example.com/"}, want: "operations.dynamics.com"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), test.want)
			}
		})
	}
}

func TestHelpDocumentsStandaloneSessionWorkflow(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	for _, want := range []string{"d365-expense", "har capture", "session import", "no submit"} {
		if !strings.Contains(strings.ToLower(strings.ReplaceAll(stdout.String(), "\n", " ")), strings.ToLower(want)) {
			t.Fatalf("help = %q, want %q", stdout.String(), want)
		}
	}
}

func TestValidateDynamicsCaptureURL(t *testing.T) {
	t.Parallel()

	for _, valid := range []string{
		"https://tenant.operations.dynamics.com/",
		"https://tenant.operations.dynamics.com/?cmp=USMF",
	} {
		if err := validateDynamicsCaptureURL(valid); err != nil {
			t.Errorf("validate(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{
		"",
		"http://tenant.operations.dynamics.com/",
		"https://operations.dynamics.com/",
		"https://tenant.operations.dynamics.com.evil.test/",
		"https://user@tenant.operations.dynamics.com/",
		"https://tenant.operations.dynamics.com/#fragment",
	} {
		if err := validateDynamicsCaptureURL(invalid); err == nil {
			t.Errorf("validate(%q) error = nil", invalid)
		}
	}
}

func TestValidateCaptureOutput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.har")
	if err := validateCaptureOutput(missing, false); err != nil {
		t.Fatalf("missing output rejected: %v", err)
	}

	existing := filepath.Join(dir, "existing.har")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateCaptureOutput(existing, false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output error = %v", err)
	}
	if err := validateCaptureOutput(existing, true); err != nil {
		t.Fatalf("forced existing output rejected: %v", err)
	}

	if runtime.GOOS != "windows" {
		symlink := filepath.Join(dir, "link.har")
		if err := os.Symlink(existing, symlink); err != nil {
			t.Fatal(err)
		}
		if err := validateCaptureOutput(symlink, true); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink output error = %v", err)
		}
	}
}

func TestRetainDynamicsProcessMessages(t *testing.T) {
	t.Parallel()

	archive := &har.Archive{}
	archive.Log.Entries = []har.Entry{
		{Request: har.Request{Method: "GET", URL: "https://tenant.operations.dynamics.com/"}},
		{Request: har.Request{Method: "POST", URL: "https://login.microsoftonline.com/Services/ReliableCommunicationManager.svc/ProcessMessages"}},
		{Request: har.Request{Method: "POST", URL: "https://tenant.operations.dynamics.com/Services/TelemetryManager.svc/ProcessEventData"}},
		{Request: har.Request{Method: "POST", URL: "https://tenant.operations.dynamics.com/Services/ReliableCommunicationManager.svc/ProcessMessages?cmp=USMF&lng=en-us"}},
	}

	count, err := retainDynamicsProcessMessages(archive, "https://tenant.operations.dynamics.com:443/")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(archive.Log.Entries) != 1 {
		t.Fatalf("count=%d entries=%d, want 1", count, len(archive.Log.Entries))
	}
	if got := archive.Log.Entries[0].Request.URL; !strings.Contains(got, "ReliableCommunicationManager") {
		t.Fatalf("retained URL = %q", got)
	}
}
