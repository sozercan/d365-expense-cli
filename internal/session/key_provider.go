package session

import (
	"encoding/base64"
	"errors"
	"fmt"

	keyring "github.com/zalando/go-keyring"
)

const keyringService = "d365-expense/session-encryption/v1"

var errEncryptionKeyNotFound = errors.New("session encryption key not found")

// KeyProvider stores small encryption keys outside the session file. The
// production implementation uses the operating system keyring.
type KeyProvider interface {
	Get(id string) ([]byte, error)
	Set(id string, key []byte) error
	Delete(id string) error
}

type systemKeyProvider struct{}

func (systemKeyProvider) Get(id string) ([]byte, error) {
	encoded, err := keyring.Get(keyringService, id)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, errEncryptionKeyNotFound
		}
		return nil, fmt.Errorf("read OS keyring: %w", err)
	}
	key, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("OS keyring contains an invalid session encryption key")
	}
	if len(key) != sessionEncryptionKeySize {
		clear(key)
		return nil, errors.New("OS keyring contains a session encryption key with an invalid size")
	}
	return key, nil
}

func (systemKeyProvider) Set(id string, key []byte) error {
	if len(key) != sessionEncryptionKeySize {
		return errors.New("session encryption key has an invalid size")
	}
	if err := keyring.Set(keyringService, id, base64.RawStdEncoding.EncodeToString(key)); err != nil {
		return fmt.Errorf("write OS keyring: %w", err)
	}
	return nil
}

func (systemKeyProvider) Delete(id string) error {
	if err := keyring.Delete(keyringService, id); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return errEncryptionKeyNotFound
		}
		return fmt.Errorf("delete OS keyring entry: %w", err)
	}
	return nil
}

func (store *Store) keyProvider() KeyProvider {
	if store != nil && store.keys != nil {
		return store.keys
	}
	return systemKeyProvider{}
}

// CleanupKey deletes an orphaned encryption key reported by a previous
// partially successful session replacement or removal.
func (store *Store) CleanupKey(id string) error {
	if err := validateEncryptionID(id); err != nil {
		return fmt.Errorf("cleanup session encryption key: %w", err)
	}
	if err := store.keyProvider().Delete(id); err != nil && !errors.Is(err, errEncryptionKeyNotFound) {
		return fmt.Errorf("cleanup session encryption key %q: %w", id, err)
	}
	return nil
}
