package session

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestSystemKeyringEncryptedStoreIntegration(t *testing.T) {
	if os.Getenv("D365_EXPENSE_KEYRING_INTEGRATION") != "1" {
		t.Skip("set D365_EXPENSE_KEYRING_INTEGRATION=1 to exercise the native OS keyring")
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := validSession(t)
	if err := store.Save("integration", want); err != nil {
		t.Fatal(err)
	}
	path, err := store.Path("integration")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := decodeEnvelope(data, "integration")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = (systemKeyProvider{}).Delete(envelope.KeyID)
	})
	loaded, err := store.Load("integration")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("round trip mismatch: got %#v want %#v", loaded, want)
	}
	if err := store.Remove("integration"); err != nil {
		t.Fatal(err)
	}
	if _, err := (systemKeyProvider{}).Get(envelope.KeyID); !errors.Is(err, errEncryptionKeyNotFound) {
		t.Fatalf("key remains after removal: %v", err)
	}
}
