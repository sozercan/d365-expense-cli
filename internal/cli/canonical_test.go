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

func TestCanonicalCreateRequiresDraftAndSource(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := runCanonical([]string{"create", "--session", "work", "--purpose", "event", "--dry-run"}, &stdout, &stderr); code != 2 {
		t.Fatalf("missing draft code = %d", code)
	}
	if !strings.Contains(stderr.String(), "--draft is required") {
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
