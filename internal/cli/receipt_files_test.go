package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
	0xde,
}

func TestReceiptInputFromPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.png")
	if err := os.WriteFile(path, tinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := receiptInputFromPath(path, 1024)
	if err != nil {
		t.Fatalf("receiptInputFromPath() error = %v", err)
	}
	if input.Filename != "receipt.png" || input.MediaType != "image/png" || input.Size != int64(len(tinyPNG)) || input.Open == nil {
		t.Fatalf("input = %#v", input)
	}
	file, err := input.Open()
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
}

func TestReceiptInputFromPathRejectsUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		file string
		data []byte
		mode os.FileMode
		max  int64
		want string
	}{
		{name: "empty", file: "empty.png", mode: 0o600, max: 1024, want: "empty"},
		{name: "extension", file: "receipt.txt", data: tinyPNG, mode: 0o600, max: 1024, want: ".png"},
		{name: "type", file: "receipt.png", data: []byte("not a png"), mode: 0o600, max: 1024, want: "not image/png"},
		{name: "size", file: "receipt.png", data: tinyPNG, mode: 0o600, max: 4, want: "exceeds limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(dir, test.name+"-"+test.file)
			if err := os.WriteFile(path, test.data, test.mode); err != nil {
				t.Fatal(err)
			}
			_, err := receiptInputFromPath(path, test.max)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReceiptInputFromPathRejectsBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	path := filepath.Join(t.TempDir(), "receipt.png")
	if err := os.WriteFile(path, tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := receiptInputFromPath(path, 1024); err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("error = %v", err)
	}
}

func TestReceiptInputUsesValidatedSnapshotAfterPathReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.png")
	if err := os.WriteFile(path, tinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := receiptInputFromPath(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	replacement := append([]byte(nil), tinyPNG...)
	replacement[len(replacement)-1] ^= 0xff
	if err := os.WriteFile(path, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := input.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, tinyPNG) {
		t.Fatalf("uploaded snapshot changed: got %x want %x", got, tinyPNG)
	}
}
