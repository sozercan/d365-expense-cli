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
	for _, want := range []string{"d365-expense", "create", "receipt attach", "session import", "session show", "har inspect", "har capture"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "create-draft") || strings.Contains(stdout.String(), "msexpense") {
		t.Fatalf("canonical help exposed legacy names:\n%s", stdout.String())
	}
}

func TestCanonicalCreateRequiresExactlyOneFinalActionAndSource(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := runCanonical([]string{"create", "--session", "work", "--purpose", "event", "--dry-run"}, &stdout, &stderr); code != 2 {
		t.Fatalf("missing final action code = %d", code)
	}
	if !strings.Contains(stderr.String(), "exactly one of --draft or --submit") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runCanonical([]string{"create", "--draft", "--submit", "--session", "work", "--purpose", "event", "--dry-run"}, &stdout, &stderr); code != 2 {
		t.Fatalf("conflicting final action code = %d", code)
	}
	if !strings.Contains(stderr.String(), "exactly one of --draft or --submit") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runCanonical([]string{"create", "--draft", "--purpose", "event", "--dry-run"}, &stdout, &stderr); code != 2 {
		t.Fatalf("missing source code = %d", code)
	}
	if !strings.Contains(stderr.String(), "exactly one of --har or --session") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCanonicalCreateSubmitRequiresConfirmationOnlyForExecution(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := runCanonical([]string{"create", "--submit", "--session", "work", "--purpose", "event"}, &stdout, &stderr); code != 2 {
		t.Fatalf("missing confirmation code = %d", code)
	}
	if !strings.Contains(stderr.String(), "requires --confirm-submit") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runCanonical([]string{"create", "--submit", "--confirm-submit", "--session", "work", "--purpose", "event", "--dry-run"}, &stdout, &stderr); code != 2 {
		t.Fatalf("dry-run confirmation code = %d", code)
	}
	if !strings.Contains(stderr.String(), "cannot be used with --dry-run") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCanonicalCreateSubmitRoutesExplicitSubmit(t *testing.T) {
	t.Parallel()
	var got []string
	runners := defaultLegacyRunners()
	runners.createDraft = func(args []string, _, _ io.Writer) int {
		got = append([]string(nil), args...)
		return 0
	}
	var stdout, stderr bytes.Buffer
	if code := runCanonicalWithRunners([]string{"create", "--submit", "--confirm-submit", "--session", "work", "--purpose", "event"}, &stdout, &stderr, runners); code != 0 {
		t.Fatalf("code = %d, stderr=%q", code, stderr.String())
	}
	want := []string{"--session", "work", "--purpose", "event", "--submit", "--confirm-submit", "--timeout", "45s", "--execute"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestCanonicalCreateSubmitDryRunNeverConfirmsOrExecutes(t *testing.T) {
	t.Parallel()
	var got []string
	runners := defaultLegacyRunners()
	runners.createDraft = func(args []string, _, _ io.Writer) int {
		got = append([]string(nil), args...)
		return 0
	}
	var stdout, stderr bytes.Buffer
	if code := runCanonicalWithRunners([]string{"create", "--submit", "--session", "work", "--purpose", "event", "--dry-run"}, &stdout, &stderr, runners); code != 0 {
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
	runners.createDraftWithReceipts = func(args []string, _, _ io.Writer) int {
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
	runners.createDraft = func(args []string, _, _ io.Writer) int {
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

func TestCanonicalCreateSubmitRoutesReceiptsAndConfirmation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	receipt := filepath.Join(dir, "receipt.png")
	if err := os.WriteFile(receipt, tinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	runners := defaultLegacyRunners()
	runners.createDraftWithReceipts = func(args []string, _, _ io.Writer) int {
		got = append([]string(nil), args...)
		return 0
	}
	var stdout, stderr bytes.Buffer
	code := runCanonicalWithRunners([]string{
		"create", "--submit", "--confirm-submit", "--session", "work", "--purpose", "event", "--receipt", receipt,
	}, &stdout, &stderr, runners)
	if code != 0 {
		t.Fatalf("code = %d, stderr=%q", code, stderr.String())
	}
	want := []string{
		"--session", "work", "--purpose", "event", "--submit", "--confirm-submit",
		"--timeout", "2m0s", "--file", receipt, "--execute",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}
