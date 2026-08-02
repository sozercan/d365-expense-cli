package capture

import (
	"bytes"
	"testing"
)

func TestTrimJSONBOM(t *testing.T) {
	want := []byte(`{"ok":true}`)
	for _, input := range [][]byte{
		append([]byte{0xef, 0xbb, 0xbf}, want...),
		append([]byte("ï»¿"), want...),
		want,
	} {
		if got := trimJSONBOM(input); !bytes.Equal(got, want) {
			t.Fatalf("trimJSONBOM(%q) = %q, want %q", input, got, want)
		}
	}
}
