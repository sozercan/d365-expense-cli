package session

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type memoryKeyProvider struct {
	mu        sync.Mutex
	keys      map[string][]byte
	getErr    error
	setErr    error
	deleteErr error
}

func newMemoryKeyProvider() *memoryKeyProvider {
	return &memoryKeyProvider{keys: make(map[string][]byte)}
}

func (provider *memoryKeyProvider) Get(id string) ([]byte, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.getErr != nil {
		return nil, provider.getErr
	}
	key, ok := provider.keys[id]
	if !ok {
		return nil, errEncryptionKeyNotFound
	}
	return append([]byte(nil), key...), nil
}

func (provider *memoryKeyProvider) Set(id string, key []byte) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.setErr != nil {
		return provider.setErr
	}
	provider.keys[id] = append([]byte(nil), key...)
	return nil
}

func (provider *memoryKeyProvider) Delete(id string) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.deleteErr != nil {
		return provider.deleteErr
	}
	if _, ok := provider.keys[id]; !ok {
		return errEncryptionKeyNotFound
	}
	delete(provider.keys, id)
	return nil
}

func TestEncryptedSaveUsesFreshNonceAndBindsSessionName(t *testing.T) {
	store := newTestStore(t)
	session := validSession(t)
	if err := store.Save("alpha", session); err != nil {
		t.Fatal(err)
	}
	alphaPath, _ := store.Path("alpha")
	first, err := os.ReadFile(alphaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("alpha", session); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(alphaPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("identical session saves produced identical ciphertext")
	}

	betaPath, _ := store.Path("beta")
	if err := os.WriteFile(betaPath, second, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("beta"); err == nil || !strings.Contains(err.Error(), "authenticate encrypted session") {
		t.Fatalf("Load(copied ciphertext) error = %v", err)
	}
	loaded, err := store.Load("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, session) {
		t.Fatalf("Load(alpha) mismatch: got %#v want %#v", loaded, session)
	}
}

func TestEncryptedLoadRejectsTamperingMissingKeyAndWrongKey(t *testing.T) {
	store := newTestStore(t)
	if err := store.Save("work", validSession(t)); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("work")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := decodeEnvelope(original, "work")
	if err != nil {
		t.Fatal(err)
	}
	provider := store.keys.(*memoryKeyProvider)
	originalKey, err := provider.Get(envelope.KeyID)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("tampered ciphertext", func(t *testing.T) {
		tamperedEnvelope := envelope
		tamperedEnvelope.Ciphertext = append([]byte(nil), envelope.Ciphertext...)
		tamperedEnvelope.Ciphertext[len(tamperedEnvelope.Ciphertext)/2] ^= 0x80
		tampered, err := encodeEnvelope(tamperedEnvelope)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, tampered, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load("work"); err == nil || !strings.Contains(err.Error(), "authenticate encrypted session") {
			t.Fatalf("Load(tampered) error = %v", err)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
		provider.mu.Lock()
		delete(provider.keys, envelope.KeyID)
		provider.mu.Unlock()
		if _, err := store.Load("work"); err == nil || !strings.Contains(err.Error(), "missing from the OS keyring") {
			t.Fatalf("Load(missing key) error = %v", err)
		}
	})

	t.Run("wrong key", func(t *testing.T) {
		provider.mu.Lock()
		provider.keys[envelope.KeyID] = bytes.Repeat([]byte{0xA5}, sessionEncryptionKeySize)
		provider.mu.Unlock()
		if _, err := store.Load("work"); err == nil || !strings.Contains(err.Error(), "authenticate encrypted session") {
			t.Fatalf("Load(wrong key) error = %v", err)
		}
		before, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		replacement := validSession(t)
		replacement.NextClientSequence++
		if err := store.Save("work", replacement); err == nil || !strings.Contains(err.Error(), "authenticate existing encrypted session") {
			t.Fatalf("Save(wrong key) error = %v", err)
		}
		after, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("failed replacement changed existing ciphertext")
		}
	})

	provider.mu.Lock()
	provider.keys[envelope.KeyID] = originalKey
	provider.mu.Unlock()
}

func TestAuthenticatedPlaintextStillUsesStrictSessionValidation(t *testing.T) {
	store := newTestStore(t)
	if err := store.Save("work", validSession(t)); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("work")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := decodeEnvelope(data, "work")
	if err != nil {
		t.Fatal(err)
	}
	provider := store.keys.(*memoryKeyProvider)
	key, err := provider.Get(envelope.KeyID)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)

	plaintext, err := encodeSession(validSession(t))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(plaintext, &document); err != nil {
		t.Fatal(err)
	}
	document["unexpected"] = true
	malformed := mustMarshal(t, document)
	envelope.Ciphertext, err = sealSession("work", envelope.KeyID, key, malformed)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("work"); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load(authenticated malformed plaintext) error = %v", err)
	}
}

func TestLegacyPlaintextLoadsWithoutRewriteAndMigratesOnSave(t *testing.T) {
	store := newTestStore(t)
	if err := store.ensureDirectories(); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("legacy")
	want := validSession(t)
	plaintext, err := encodeSession(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, plaintext, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("legacy Load mismatch: got %#v want %#v", loaded, want)
	}
	afterRead, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, afterRead) {
		t.Fatal("read-only legacy load rewrote the session")
	}

	loaded.Status = StatusInProgress
	if err := store.Save("legacy", loaded); err != nil {
		t.Fatal(err)
	}
	afterSave, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(afterSave, []byte(encryptedSessionMagic)) {
		t.Fatal("legacy session was not migrated to encrypted storage")
	}
	for _, secret := range []string{"header-secret", "cookie-secret", "new-report-target"} {
		if bytes.Contains(afterSave, []byte(secret)) {
			t.Fatalf("migrated file leaked %q", secret)
		}
	}
	migrated, err := store.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Status != StatusInProgress || migrated.NextClientSequence != want.NextClientSequence {
		t.Fatalf("migrated session = %#v", migrated)
	}
}

func TestKeyringFailureFailsClosedWithoutPlaintextFile(t *testing.T) {
	store := newTestStore(t)
	provider := store.keys.(*memoryKeyProvider)
	provider.setErr = errors.New("keyring locked")
	path, _ := store.Path("work")
	if err := store.Save("work", validSession(t)); err == nil || !strings.Contains(err.Error(), "persist encryption key") {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session file exists after keyring failure: %v", err)
	}
}

func TestEncryptedEnvelopeRejectsUnknownFieldsTrailingDataAndVersion(t *testing.T) {
	store := newTestStore(t)
	if err := store.Save("work", validSession(t)); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("work")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	magic := []byte(encryptedSessionMagic)
	var document map[string]any
	if err := jsonUnmarshalStrictEnough(original[len(magic):], &document); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(map[string]any) []byte
		want   string
	}{
		{
			name: "unknown field",
			mutate: func(value map[string]any) []byte {
				value["unexpected"] = true
				return append(magic, mustMarshal(t, value)...)
			},
			want: "unknown field",
		},
		{
			name: "trailing data",
			mutate: func(value map[string]any) []byte {
				return append(append(magic, mustMarshal(t, value)...), []byte("\n{}")...)
			},
			want: "trailing JSON",
		},
		{
			name: "unsupported version",
			mutate: func(value map[string]any) []byte {
				value["version"] = float64(encryptedSessionVersion + 1)
				return append(magic, mustMarshal(t, value)...)
			},
			want: "envelope version",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copyDocument := make(map[string]any, len(document))
			for key, value := range document {
				copyDocument[key] = value
			}
			if err := os.WriteFile(path, test.mutate(copyDocument), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load("work"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func jsonUnmarshalStrictEnough(data []byte, destination any) error {
	return json.Unmarshal(data, destination)
}

func TestSystemKeyProviderEncodingRoundTripContract(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, sessionEncryptionKeySize)
	encoded := base64.RawStdEncoding.EncodeToString(key)
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, key) {
		t.Fatal("key encoding round trip mismatch")
	}
}

func TestRemoveDoesNotRequireDecryptingSession(t *testing.T) {
	store := newTestStore(t)
	if err := store.Save("work", validSession(t)); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("work")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0x01
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	provider := store.keys.(*memoryKeyProvider)
	provider.getErr = errors.New("keyring unavailable")
	if err := store.Remove("work"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session file remains after Remove: %v", err)
	}
}

func TestEncryptedFilenameRemainsCompatible(t *testing.T) {
	store := newTestStore(t)
	path, err := store.Path("work")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "work.session.json" {
		t.Fatalf("Path() = %q", path)
	}
}

type mismatchedReadbackProvider struct{}

func (mismatchedReadbackProvider) Get(string) ([]byte, error) {
	return bytes.Repeat([]byte{0x7F}, sessionEncryptionKeySize), nil
}
func (mismatchedReadbackProvider) Set(string, []byte) error { return nil }
func (mismatchedReadbackProvider) Delete(string) error      { return nil }

func TestEncryptionKeyReadbackMismatchFailsClosed(t *testing.T) {
	store, err := NewStore(t.TempDir(), WithKeyProvider(mismatchedReadbackProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("work")
	if err := store.Save("work", validSession(t)); err == nil || !strings.Contains(err.Error(), "verification mismatch") {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session file exists after key verification failure: %v", err)
	}
}

func TestPostCommitDirectorySyncFailureKeepsInstalledCiphertextDecryptable(t *testing.T) {
	store := newTestStore(t)
	store.syncDir = func(string) error { return errors.New("directory sync failed") }
	want := validSession(t)
	if err := store.Save("work", want); err == nil || !strings.Contains(err.Error(), "committed rename") {
		t.Fatalf("Save() error = %v", err)
	}
	store.syncDir = nil
	loaded, err := store.Load("work")
	if err != nil {
		t.Fatalf("committed session is unreadable after sync error: %v", err)
	}
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("committed session mismatch: got %#v want %#v", loaded, want)
	}
}

func TestReplaceIsTransactionalAndRetiresOnlyAuthenticatedOldKey(t *testing.T) {
	store := newTestStore(t)
	provider := store.keys.(*memoryKeyProvider)
	oldSession := validSession(t)
	if err := store.Save("work", oldSession); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("work")
	oldBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oldEnvelope, err := decodeEnvelope(oldBytes, "work")
	if err != nil {
		t.Fatal(err)
	}

	replacement := cloneSession(t, oldSession)
	replacement.NextClientSequence++
	provider.setErr = errors.New("keyring locked")
	if err := store.Replace("work", replacement); err == nil {
		t.Fatal("Replace() error = nil")
	}
	provider.setErr = nil
	afterFailure, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterFailure, oldBytes) {
		t.Fatal("failed replacement changed the existing session file")
	}
	loaded, err := store.Load("work")
	if err != nil || !reflect.DeepEqual(loaded, oldSession) {
		t.Fatalf("old session was not preserved: loaded=%#v err=%v", loaded, err)
	}

	if err := store.Replace("work", replacement); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load("work")
	if err != nil || !reflect.DeepEqual(loaded, replacement) {
		t.Fatalf("replacement mismatch: loaded=%#v err=%v", loaded, err)
	}
	if _, err := provider.Get(oldEnvelope.KeyID); !errors.Is(err, errEncryptionKeyNotFound) {
		t.Fatalf("authenticated old key was not retired: %v", err)
	}
}

func TestRemoveCopiedCiphertextDoesNotDeleteOriginalSessionKey(t *testing.T) {
	store := newTestStore(t)
	want := validSession(t)
	if err := store.Save("alpha", want); err != nil {
		t.Fatal(err)
	}
	alphaPath, _ := store.Path("alpha")
	alphaBytes, err := os.ReadFile(alphaPath)
	if err != nil {
		t.Fatal(err)
	}
	betaPath, _ := store.Path("beta")
	if err := os.WriteFile(betaPath, alphaBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("beta"); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("alpha")
	if err != nil {
		t.Fatalf("removing copied ciphertext deleted alpha key: %v", err)
	}
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("alpha mismatch: got %#v want %#v", loaded, want)
	}
}

func TestSessionAssociatedDataBindsExactSessionName(t *testing.T) {
	keyID := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, sessionEncryptionIDSize))
	if bytes.Equal(sessionAssociatedData("Travel", keyID), sessionAssociatedData("travel", keyID)) {
		t.Fatal("associated data discarded session-name case")
	}
}

func TestRequireExactSessionNameRejectsCaseMismatch(t *testing.T) {
	store := newTestStore(t)
	if err := store.ensureDirectories(); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("Travel")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.requireExactSessionName("travel"); err == nil || !strings.Contains(err.Error(), `stored as "Travel"`) {
		t.Fatalf("requireExactSessionName() error = %v", err)
	}
}

func TestRemoveReportsKeyCleanupFailureAfterDeletingCiphertext(t *testing.T) {
	store := newTestStore(t)
	provider := store.keys.(*memoryKeyProvider)
	if err := store.Save("work", validSession(t)); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("work")
	provider.deleteErr = errors.New("keyring locked")
	if err := store.Remove("work"); err == nil || !strings.Contains(err.Error(), "session file was removed") {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ciphertext remains after partial removal: %v", err)
	}
}

func TestReplaceReportsOldKeyRetirementFailureWithoutLosingReplacement(t *testing.T) {
	store := newTestStore(t)
	provider := store.keys.(*memoryKeyProvider)
	if err := store.Save("work", validSession(t)); err != nil {
		t.Fatal(err)
	}
	replacement := validSession(t)
	replacement.NextClientSequence++
	provider.deleteErr = errors.New("keyring locked")
	if err := store.Replace("work", replacement); err == nil || !strings.Contains(err.Error(), "replacement committed") {
		t.Fatalf("Replace() error = %v", err)
	}
	provider.deleteErr = nil
	loaded, err := store.Load("work")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, replacement) {
		t.Fatalf("committed replacement mismatch: got %#v want %#v", loaded, replacement)
	}
}

func TestCleanupKeySupportsRetryAfterPartialCleanup(t *testing.T) {
	store := newTestStore(t)
	provider := store.keys.(*memoryKeyProvider)
	keyID, key, err := newSessionEncryptionMaterial()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	if err := provider.Set(keyID, key); err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupKey(keyID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Get(keyID); !errors.Is(err, errEncryptionKeyNotFound) {
		t.Fatalf("key remains after CleanupKey: %v", err)
	}
	if err := store.CleanupKey(keyID); err != nil {
		t.Fatalf("CleanupKey missing key error = %v", err)
	}
	if err := store.CleanupKey("not-a-valid-key-id"); err == nil {
		t.Fatal("CleanupKey(invalid) error = nil")
	}
}

type mismatchAndDeleteFailureProvider struct{}

func (mismatchAndDeleteFailureProvider) Get(string) ([]byte, error) {
	return bytes.Repeat([]byte{0x66}, sessionEncryptionKeySize), nil
}
func (mismatchAndDeleteFailureProvider) Set(string, []byte) error { return nil }
func (mismatchAndDeleteFailureProvider) Delete(string) error {
	return errors.New("keyring delete failed")
}

func TestKeyVerificationCleanupFailureReportsSupportedRecovery(t *testing.T) {
	store, err := NewStore(t.TempDir(), WithKeyProvider(mismatchAndDeleteFailureProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("work", validSession(t)); err == nil || !strings.Contains(err.Error(), "session cleanup-key") {
		t.Fatalf("Save() error = %v", err)
	}
}

func TestReplacePostCommitSyncFailureReportsRetainedOldKey(t *testing.T) {
	store := newTestStore(t)
	if err := store.Save("work", validSession(t)); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("work")
	oldBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oldEnvelope, err := decodeEnvelope(oldBytes, "work")
	if err != nil {
		t.Fatal(err)
	}
	replacement := validSession(t)
	replacement.NextClientSequence++
	store.syncDir = func(string) error { return errors.New("directory sync failed") }
	err = store.Replace("work", replacement)
	if err == nil || !strings.Contains(err.Error(), oldEnvelope.KeyID) || !strings.Contains(err.Error(), "session cleanup-key") {
		t.Fatalf("Replace() error = %v", err)
	}
	store.syncDir = nil
	loaded, err := store.Load("work")
	if err != nil || !reflect.DeepEqual(loaded, replacement) {
		t.Fatalf("committed replacement unavailable: loaded=%#v err=%v", loaded, err)
	}
}

func TestRemoveSyncFailureReportsRetainedKey(t *testing.T) {
	store := newTestStore(t)
	if err := store.Save("work", validSession(t)); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("work")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := decodeEnvelope(data, "work")
	if err != nil {
		t.Fatal(err)
	}
	store.syncDir = func(string) error { return errors.New("directory sync failed") }
	err = store.Remove("work")
	if err == nil || !strings.Contains(err.Error(), envelope.KeyID) || !strings.Contains(err.Error(), "session cleanup-key") {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("session file remains after committed remove: %v", statErr)
	}
}
