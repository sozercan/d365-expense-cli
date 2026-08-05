package session

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	encryptedSessionMagic    = "D365-EXPENSE-SESSION\x00"
	encryptedSessionVersion  = 1
	sessionEncryptionKeySize = 32
	sessionEncryptionIDSize  = 16
)

type encryptedSessionEnvelope struct {
	Version    int    `json:"version"`
	KeyID      string `json:"keyId"`
	Ciphertext []byte `json:"ciphertext"`
}

// Save atomically encrypts and writes a validated session with mode 0600. The
// encryption key is stored separately in the operating system keyring.
func (store *Store) Save(name string, session *Session) error {
	path, err := store.Path(name)
	if err != nil {
		return err
	}
	if err := session.Validate(); err != nil {
		return fmt.Errorf("save session %q: %w", name, err)
	}
	if err := store.ensureDirectories(); err != nil {
		return err
	}

	exists, err := inspectOptionalRegularFile(path, name, "save")
	if err != nil {
		return err
	}
	var keyID string
	var key []byte
	createdKey := false
	if exists {
		if err := store.requireExactSessionName(name); err != nil {
			return err
		}
		data, err := readPrivateSessionFile(path, name, maxEncryptedSessionFile)
		if err != nil {
			return err
		}
		if bytes.HasPrefix(data, []byte(encryptedSessionMagic)) {
			envelope, err := decodeEnvelope(data, name)
			clear(data)
			if err != nil {
				return err
			}
			keyID = envelope.KeyID
			key, err = store.keyProvider().Get(keyID)
			if err != nil {
				return sessionKeyError("save", name, err)
			}
			previous, err := openSession(name, keyID, key, envelope.Ciphertext)
			if err != nil {
				clear(key)
				return fmt.Errorf("save session %q: authenticate existing encrypted session before replacement: %w", name, err)
			}
			clear(previous)
		} else {
			clear(data)
			keyID, key, err = store.createEncryptionMaterial(name)
			if err != nil {
				return err
			}
			createdKey = true
		}
	} else {
		keyID, key, err = store.createEncryptionMaterial(name)
		if err != nil {
			return err
		}
		createdKey = true
	}
	defer clear(key)

	committed, err := store.writeEncrypted(name, path, keyID, key, session)
	if err != nil {
		if createdKey && !committed {
			return store.joinKeyCleanupError(err, keyID, "new session key")
		}
		return err
	}
	return nil
}

// Replace atomically installs a newly encrypted session even when the current
// file is corrupt or its key is unavailable. The previous key is retired only
// after its ciphertext was authenticated for this name and replacement fully
// committed.
func (store *Store) Replace(name string, session *Session) error {
	path, err := store.Path(name)
	if err != nil {
		return err
	}
	if err := session.Validate(); err != nil {
		return fmt.Errorf("replace session %q: %w", name, err)
	}
	if err := store.ensureDirectories(); err != nil {
		return err
	}
	exists, err := inspectOptionalRegularFile(path, name, "replace")
	if err != nil {
		return err
	}
	if !exists {
		return store.Save(name, session)
	}
	if err := store.requireExactSessionName(name); err != nil {
		return err
	}

	oldKeyID := ""
	if data, readErr := readPrivateSessionFile(path, name, maxEncryptedSessionFile); readErr == nil {
		if envelope, envelopeErr := decodeEnvelope(data, name); envelopeErr == nil {
			if key, keyErr := store.keyProvider().Get(envelope.KeyID); keyErr == nil {
				if plaintext, openErr := openSession(name, envelope.KeyID, key, envelope.Ciphertext); openErr == nil {
					oldKeyID = envelope.KeyID
					clear(plaintext)
				}
				clear(key)
			}
		}
		clear(data)
	}

	newKeyID, key, err := store.createEncryptionMaterial(name)
	if err != nil {
		return err
	}
	defer clear(key)
	committed, err := store.writeEncrypted(name, path, newKeyID, key, session)
	if err != nil {
		if !committed {
			return store.joinKeyCleanupError(err, newKeyID, "uncommitted replacement key")
		}
		if oldKeyID != "" {
			return errors.Join(err, fmt.Errorf(
				"replace session %q: previous key %q was retained because replacement durability was not confirmed; after verifying the installed session, run `d365-expense session cleanup-key %s`",
				name, oldKeyID, oldKeyID,
			))
		}
		return err
	}
	if oldKeyID != "" && oldKeyID != newKeyID {
		if err := store.keyProvider().Delete(oldKeyID); err != nil && !errors.Is(err, errEncryptionKeyNotFound) {
			return fmt.Errorf("replace session %q: replacement committed but previous key %q could not be retired: %w", name, oldKeyID, err)
		}
	}
	return nil
}

// Load opens one private session file, decrypting encrypted sessions and still
// accepting the legacy plaintext JSON format. Legacy files are migrated by the
// next locked Save, before a mutating command sends its first network request.
func (store *Store) Load(name string) (*Session, error) {
	path, err := store.Path(name)
	if err != nil {
		return nil, err
	}
	if err := store.checkDirectories(); err != nil {
		return nil, err
	}
	exists, err := inspectOptionalRegularFile(path, name, "load")
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("load session %q: %w", name, os.ErrNotExist)
	}
	if err := store.requireExactSessionName(name); err != nil {
		return nil, err
	}
	data, err := readPrivateSessionFile(path, name, maxEncryptedSessionFile)
	if err != nil {
		return nil, err
	}
	defer clear(data)

	if bytes.HasPrefix(data, []byte(encryptedSessionMagic)) {
		return store.decryptSession(name, data)
	}
	if bytes.HasPrefix([]byte(encryptedSessionMagic), data) {
		return nil, fmt.Errorf("load session %q: encrypted session header is truncated", name)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("load session %q: unrecognized session file format", name)
	}
	if len(data) > maxSessionPlaintext {
		return nil, fmt.Errorf("load session %q: legacy plaintext exceeds %d bytes", name, maxSessionPlaintext)
	}
	return decodeSession(data, name)
}

func (store *Store) writeEncrypted(name, path, keyID string, key []byte, session *Session) (bool, error) {
	plaintext, err := encodeSession(session)
	if err != nil {
		return false, fmt.Errorf("save session %q: encode plaintext: %w", name, err)
	}
	defer clear(plaintext)
	if len(plaintext) > maxSessionPlaintext {
		return false, fmt.Errorf("save session %q: encoded session exceeds %d bytes", name, maxSessionPlaintext)
	}

	ciphertext, err := sealSession(name, keyID, key, plaintext)
	if err != nil {
		return false, fmt.Errorf("save session %q: encrypt: %w", name, err)
	}
	defer clear(ciphertext)
	envelope := encryptedSessionEnvelope{
		Version:    encryptedSessionVersion,
		KeyID:      keyID,
		Ciphertext: ciphertext,
	}
	encoded, err := encodeEnvelope(envelope)
	if err != nil {
		return false, fmt.Errorf("save session %q: encode encrypted envelope: %w", name, err)
	}
	defer clear(encoded)
	if len(encoded) > maxEncryptedSessionFile {
		return false, fmt.Errorf("save session %q: encrypted session exceeds %d bytes", name, maxEncryptedSessionFile)
	}
	committed, err := writePrivateAtomic(path, encoded, store.directorySync)
	if err != nil {
		return committed, fmt.Errorf("save session %q: %w", name, err)
	}
	return true, nil
}

func (store *Store) decryptSession(name string, data []byte) (*Session, error) {
	envelope, err := decodeEnvelope(data, name)
	if err != nil {
		return nil, err
	}
	key, err := store.keyProvider().Get(envelope.KeyID)
	if err != nil {
		return nil, sessionKeyError("load", name, err)
	}
	defer clear(key)
	plaintext, err := openSession(name, envelope.KeyID, key, envelope.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("load session %q: authenticate encrypted session: %w", name, err)
	}
	defer clear(plaintext)
	if len(plaintext) > maxSessionPlaintext {
		return nil, fmt.Errorf("load session %q: decrypted session exceeds %d bytes", name, maxSessionPlaintext)
	}
	return decodeSession(plaintext, name)
}

func sealSession(name, keyID string, key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nil, plaintext, sessionAssociatedData(name, keyID)), nil
}

func openSession(name, keyID string, key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nil, ciphertext, sessionAssociatedData(name, keyID))
}

func encodeEnvelope(envelope encryptedSessionEnvelope) ([]byte, error) {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(envelope); err != nil {
		return nil, err
	}
	result := make([]byte, 0, len(encryptedSessionMagic)+body.Len())
	result = append(result, encryptedSessionMagic...)
	result = append(result, body.Bytes()...)
	return result, nil
}

func decodeEnvelope(data []byte, name string) (encryptedSessionEnvelope, error) {
	magic := []byte(encryptedSessionMagic)
	if !bytes.HasPrefix(data, magic) {
		return encryptedSessionEnvelope{}, fmt.Errorf("load session %q: encrypted session header is invalid", name)
	}
	body := data[len(magic):]
	var envelope encryptedSessionEnvelope
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return encryptedSessionEnvelope{}, fmt.Errorf("load session %q: decode encrypted envelope: %w", name, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return encryptedSessionEnvelope{}, fmt.Errorf("load session %q: encrypted envelope %w", name, err)
	}
	if envelope.Version != encryptedSessionVersion {
		return encryptedSessionEnvelope{}, fmt.Errorf("load session %q: encrypted envelope version must be %d", name, encryptedSessionVersion)
	}
	if err := validateEncryptionID(envelope.KeyID); err != nil {
		return encryptedSessionEnvelope{}, fmt.Errorf("load session %q: encrypted envelope key ID: %w", name, err)
	}
	if len(envelope.Ciphertext) == 0 {
		return encryptedSessionEnvelope{}, fmt.Errorf("load session %q: encrypted envelope ciphertext is empty", name)
	}
	return envelope, nil
}

func encodeSession(session *Session) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(session); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func decodeSession(data []byte, name string) (*Session, error) {
	var result Session
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("load session %q: decode: %w", name, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("load session %q: %w", name, err)
	}
	if err := result.Validate(); err != nil {
		return nil, fmt.Errorf("load session %q: validate: %w", name, err)
	}
	return &result, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("contains a trailing JSON value")
		}
		return fmt.Errorf("contains trailing data: %w", err)
	}
	return nil
}

func newSessionEncryptionMaterial() (string, []byte, error) {
	idBytes := make([]byte, sessionEncryptionIDSize)
	if _, err := io.ReadFull(rand.Reader, idBytes); err != nil {
		return "", nil, err
	}
	key := make([]byte, sessionEncryptionKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		clear(idBytes)
		return "", nil, err
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	clear(idBytes)
	return id, key, nil
}

func (store *Store) createEncryptionMaterial(name string) (string, []byte, error) {
	keyID, key, err := newSessionEncryptionMaterial()
	if err != nil {
		return "", nil, fmt.Errorf("save session %q: generate encryption material: %w", name, err)
	}
	if err := store.keyProvider().Set(keyID, key); err != nil {
		clear(key)
		return "", nil, fmt.Errorf("save session %q: persist encryption key: %w", name, err)
	}
	readBack, err := store.keyProvider().Get(keyID)
	if err != nil {
		clear(key)
		primary := fmt.Errorf("save session %q: verify persisted encryption key: %w", name, err)
		return "", nil, store.joinKeyCleanupError(primary, keyID, "unverified session key")
	}
	defer clear(readBack)
	if !bytes.Equal(key, readBack) {
		clear(key)
		primary := fmt.Errorf("save session %q: persisted encryption key verification mismatch", name)
		return "", nil, store.joinKeyCleanupError(primary, keyID, "mismatched session key")
	}
	return keyID, key, nil
}

func (store *Store) joinKeyCleanupError(primary error, keyID, label string) error {
	cleanupErr := store.keyProvider().Delete(keyID)
	if cleanupErr == nil || errors.Is(cleanupErr, errEncryptionKeyNotFound) {
		return primary
	}
	return errors.Join(primary, fmt.Errorf(
		"%s %q could not be removed from the OS keyring; run `d365-expense session cleanup-key %s`: %w",
		label, keyID, keyID, cleanupErr,
	))
}

func validateEncryptionID(id string) error {
	decoded, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(decoded) != sessionEncryptionIDSize || base64.RawURLEncoding.EncodeToString(decoded) != id {
		clear(decoded)
		return errors.New("is invalid")
	}
	clear(decoded)
	return nil
}

func sessionAssociatedData(name, keyID string) []byte {
	return []byte(fmt.Sprintf("%s\x00%d\x00%s\x00%s", encryptedSessionMagic, encryptedSessionVersion, name, keyID))
}

func sessionKeyError(operation, name string, err error) error {
	if errors.Is(err, errEncryptionKeyNotFound) {
		return fmt.Errorf("%s session %q: encryption key is missing from the OS keyring; remove and re-import the session: %w", operation, name, err)
	}
	return fmt.Errorf("%s session %q: access OS keyring: %w", operation, name, err)
}

func readPrivateSessionFile(path, name string, maximum int64) ([]byte, error) {
	file, err := openPrivateRegularFile(path, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := readLimited(file, maximum)
	if err != nil {
		return nil, fmt.Errorf("load session %q: read: %w", name, err)
	}
	return data, nil
}

func readLimited(reader io.Reader, maximum int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		clear(data)
		return nil, fmt.Errorf("file exceeds %d bytes", maximum)
	}
	return data, nil
}

func writePrivateAtomic(path string, data []byte, syncDir func(string) error) (bool, error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, temporaryFilePattern)
	if err != nil {
		return false, fmt.Errorf("create encrypted temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	closeWithError := func(operation string, operationErr error) error {
		_ = temporary.Close()
		return fmt.Errorf("%s: %w", operation, operationErr)
	}
	if err := temporary.Chmod(0o600); err != nil {
		return false, closeWithError("restrict encrypted temporary file", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return false, closeWithError("write encrypted temporary file", err)
	}
	if err := temporary.Sync(); err != nil {
		return false, closeWithError("sync encrypted temporary file", err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("close encrypted temporary file: %w", err)
	}
	if err := replaceSessionFile(temporaryPath, path); err != nil {
		return false, fmt.Errorf("atomically replace encrypted session: %w", err)
	}
	removeTemporary = false
	if err := syncDir(directory); err != nil {
		return true, fmt.Errorf("sync session directory after committed rename: %w", err)
	}
	return true, nil
}

func (store *Store) directorySync(path string) error {
	if store != nil && store.syncDir != nil {
		return store.syncDir(path)
	}
	return syncDirectory(path)
}

func inspectOptionalRegularFile(path, name, operation string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s session %q: inspect: %w", operation, name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%s session %q: refusing to use a symbolic link", operation, name)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s session %q: path is not a regular file", operation, name)
	}
	if err := checkPrivateFileMode(path, info); err != nil {
		return false, fmt.Errorf("%s session %q: %w", operation, name, err)
	}
	return true, nil
}
