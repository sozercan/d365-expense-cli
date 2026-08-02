package har

import (
	"bytes"
	"os"
	"testing"
)

func loadSynthetic(t *testing.T) *Archive {
	t.Helper()
	archive, err := LoadFile("testdata/synthetic.json")
	if err != nil {
		t.Fatalf("LoadFile(synthetic.json) error = %v", err)
	}
	return archive
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	want, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}
