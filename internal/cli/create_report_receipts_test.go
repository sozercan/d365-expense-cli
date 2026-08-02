package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReceiptInputsFromPathsPreserveOrderAndNotes(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "first.png"),
		filepath.Join(dir, "second.png"),
	}
	for index, path := range paths {
		data := append([]byte(nil), tinyPNG...)
		data = append(data, byte(index))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	inputs, err := receiptInputsFromPaths(paths, "event travel", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 2 {
		t.Fatalf("len(inputs) = %d, want 2", len(inputs))
	}
	for index, input := range inputs {
		if input.Notes != "event travel" || input.Receipt.Filename != filepath.Base(paths[index]) {
			t.Fatalf("input %d = %#v", index, input)
		}
		reader, err := input.Receipt.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data[:len(tinyPNG)], tinyPNG) || data[len(data)-1] != byte(index) {
			t.Fatalf("receipt %d data changed", index)
		}
	}
}

func TestLegacyCreateDraftWithReceiptsRequiresFilesAndBoundsCount(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"create-draft-with-receipts", "--session", "work", "--purpose", "event"}, &stdout, &stderr); code != 2 {
		t.Fatalf("missing files exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "at least one --file") {
		t.Fatalf("stderr = %q", stderr.String())
	}

	args := []string{"create-draft-with-receipts", "--har", "unused.har", "--purpose", "event"}
	for index := 0; index < maxCLIReceipts+1; index++ {
		args = append(args, "--file", fmt.Sprintf("receipt-%d.png", index))
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(args, &stdout, &stderr); code != 2 {
		t.Fatalf("too many files exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "at most") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
