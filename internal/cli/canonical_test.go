package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCanonicalHelpShowsStructuredCommands(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := runCanonical([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"d365-expense", "create", "receipt attach", "session import", "session show", "session cleanup-key", "har inspect", "har capture"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "create-draft") || strings.Contains(stdout.String(), "msexpense") {
		t.Fatalf("canonical help exposed legacy names:\n%s", stdout.String())
	}
}

func TestCanonicalSessionCleanupKeyRoutesReportedID(t *testing.T) {
	t.Parallel()
	var got []string
	runners := defaultLegacyRunners()
	runners.sessionCleanupKey = func(args []string, _, _ io.Writer) int {
		got = append([]string(nil), args...)
		return 0
	}
	var stdout, stderr bytes.Buffer
	if code := runCanonicalWithRunners([]string{"session", "cleanup-key", "reported-key-id"}, &stdout, &stderr, runners); code != 0 {
		t.Fatalf("code = %d, stderr=%q", code, stderr.String())
	}
	if want := []string{"--id", "reported-key-id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestCanonicalCreateRequiresSource(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := runCanonical([]string{"create", "--purpose", "event", "--dry-run"}, &stdout, &stderr); code != 2 {
		t.Fatalf("missing source code = %d", code)
	}
	if !strings.Contains(stderr.String(), "exactly one of --har or --session") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCanonicalCreateSubmitsByDefault(t *testing.T) {
	t.Parallel()
	var got []string
	runners := defaultLegacyRunners()
	runners.createReport = func(args []string, _, _ io.Writer) int {
		got = append([]string(nil), args...)
		return 0
	}
	var stdout, stderr bytes.Buffer
	if code := runCanonicalWithRunners([]string{"create", "--session", "work", "--purpose", "event"}, &stdout, &stderr, runners); code != 0 {
		t.Fatalf("code = %d, stderr=%q", code, stderr.String())
	}
	want := []string{"--session", "work", "--purpose", "event", "--submit", "--timeout", "45s", "--execute"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestCanonicalCreateDefaultSubmitDryRunNeverExecutes(t *testing.T) {
	t.Parallel()
	var got []string
	runners := defaultLegacyRunners()
	runners.createReport = func(args []string, _, _ io.Writer) int {
		got = append([]string(nil), args...)
		return 0
	}
	var stdout, stderr bytes.Buffer
	if code := runCanonicalWithRunners([]string{"create", "--session", "work", "--purpose", "event", "--dry-run"}, &stdout, &stderr, runners); code != 0 {
		t.Fatalf("code = %d, stderr=%q", code, stderr.String())
	}
	want := []string{"--session", "work", "--purpose", "event", "--submit", "--timeout", "45s"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestCanonicalCreateRoutesReceiptsInOrderAndExecutesByDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	one := filepath.Join(dir, "one.png")
	two := filepath.Join(dir, "two.png")
	for _, path := range []string{one, two} {
		if err := os.WriteFile(path, tinyPNG, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	runners := defaultLegacyRunners()
	runners.createReportWithReceipts = func(args []string, _, _ io.Writer) int {
		got = append([]string(nil), args...)
		return 0
	}
	var stdout, stderr bytes.Buffer
	code := runCanonicalWithRunners([]string{
		"create", "--draft", "--session", "work", "--purpose", "event",
		"--receipt", one, "--receipt", two, "--receipt-note", "travel",
	}, &stdout, &stderr, runners)
	if code != 0 {
		t.Fatalf("code = %d, stderr=%q", code, stderr.String())
	}
	want := []string{"--session", "work", "--purpose", "event", "--timeout", "2m0s", "--file", one, "--file", two, "--notes", "travel", "--execute"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestCanonicalCreateDryRunOmitsExecute(t *testing.T) {
	t.Parallel()
	var got []string
	runners := defaultLegacyRunners()
	runners.createReport = func(args []string, _, _ io.Writer) int {
		got = append([]string(nil), args...)
		return 0
	}
	var stdout, stderr bytes.Buffer
	if code := runCanonicalWithRunners([]string{"create", "--draft", "--session", "work", "--purpose", "event", "--dry-run"}, &stdout, &stderr, runners); code != 0 {
		t.Fatalf("code = %d, stderr=%q", code, stderr.String())
	}
	for _, arg := range got {
		if arg == "--execute" {
			t.Fatalf("dry run included --execute: %#v", got)
		}
	}
}

func TestCanonicalCreateSubmitsReceiptsByDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	receipt := filepath.Join(dir, "receipt.png")
	if err := os.WriteFile(receipt, tinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	runners := defaultLegacyRunners()
	runners.createReportWithReceipts = func(args []string, _, _ io.Writer) int {
		got = append([]string(nil), args...)
		return 0
	}
	var stdout, stderr bytes.Buffer
	code := runCanonicalWithRunners([]string{
		"create", "--session", "work", "--purpose", "event", "--receipt", receipt,
	}, &stdout, &stderr, runners)
	if code != 0 {
		t.Fatalf("code = %d, stderr=%q", code, stderr.String())
	}
	want := []string{
		"--session", "work", "--purpose", "event", "--submit",
		"--timeout", "2m0s", "--file", receipt, "--execute",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestLegacyReplacementPreservesCreateSubmitIntent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "draft", args: []string{"create-draft"}, want: "d365-expense create --draft"},
		{name: "submit", args: []string{"create-draft", "--submit"}, want: "d365-expense create"},
		{name: "singular receipt submit", args: []string{"create-draft-with-receipt", "--submit=true"}, want: "d365-expense create"},
		{name: "plural receipts submit", args: []string{"create-draft-with-receipts", "-submit"}, want: "d365-expense create"},
		{name: "explicit draft", args: []string{"create-draft-with-receipts", "--submit=false"}, want: "d365-expense create --draft"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := legacyReplacement(test.args); got != test.want {
				t.Fatalf("legacyReplacement(%#v) = %q, want %q", test.args, got, test.want)
			}
		})
	}
}
