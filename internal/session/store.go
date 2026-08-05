package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	canonicalConfigEnvironment = "D365_EXPENSE_CONFIG_DIR"
	legacyConfigEnvironment    = "MSEXPENSE_CONFIG_DIR"
	canonicalConfigDirectory   = "d365-expense"
	legacyConfigDirectory      = ".msexpense"
	sessionFileSuffix          = ".session.json"
	temporaryFilePattern       = ".d365-expense-session-*"
	maxSessionPlaintext        = 1 << 20
	maxEncryptedSessionFile    = 2 << 20
)

// Store manages named encrypted session files and their OS-keyring keys.
type Store struct {
	// Dir is the directory containing session files. Direct construction is
	// supported for tests; DefaultStore and NewStore also protect ConfigDir.
	Dir string

	configDir string
	keys      KeyProvider
	syncDir   func(string) error
}

// StoreOption customizes a Store. It primarily supports deterministic tests
// without contacting the developer's real operating system keyring.
type StoreOption func(*Store) error

// WithKeyProvider replaces the OS-keyring provider for this Store.
func WithKeyProvider(provider KeyProvider) StoreOption {
	return func(store *Store) error {
		if provider == nil {
			return errors.New("session key provider is nil")
		}
		store.keys = provider
		return nil
	}
}

// DefaultStore resolves exactly one session store. D365_EXPENSE_CONFIG_DIR has
// highest precedence. MSEXPENSE_CONFIG_DIR remains a compatibility fallback
// when the canonical variable is unset. With neither variable set, new stores
// live under os.UserConfigDir()/d365-expense. An existing ~/.msexpense session
// store is used only when the canonical default contains no sessions. The two
// default stores are never merged; if both contain sessions, resolution fails
// and the caller must select one explicitly with D365_EXPENSE_CONFIG_DIR.
func DefaultStore(options ...StoreOption) (*Store, error) {
	store, err := resolveDefaultStore(os.Getenv, os.UserConfigDir, os.UserHomeDir)
	if err != nil {
		return nil, err
	}
	if err := applyStoreOptions(store, options); err != nil {
		return nil, err
	}
	return store, nil
}

func resolveDefaultStore(
	getenv func(string) string,
	userConfigDir func() (string, error),
	userHomeDir func() (string, error),
) (*Store, error) {
	if base := strings.TrimSpace(getenv(canonicalConfigEnvironment)); base != "" {
		return NewStore(base)
	}
	if base := strings.TrimSpace(getenv(legacyConfigEnvironment)); base != "" {
		return NewStore(base)
	}

	configDir, err := userConfigDir()
	if err != nil {
		return nil, fmt.Errorf("session: resolve user config directory: %w", err)
	}
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		return nil, errors.New("session: resolved user config directory is empty")
	}
	canonical := filepath.Join(configDir, canonicalConfigDirectory)

	home, err := userHomeDir()
	if err != nil {
		// The canonical store remains usable even if the legacy home-based
		// compatibility path cannot be resolved.
		return NewStore(canonical)
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return NewStore(canonical)
	}
	legacy := filepath.Join(home, legacyConfigDirectory)
	return defaultStoreForRoots(canonical, legacy)
}

func defaultStoreForRoots(canonical, legacy string) (*Store, error) {
	canonical = filepath.Clean(strings.TrimSpace(canonical))
	legacy = filepath.Clean(strings.TrimSpace(legacy))
	if canonical == "." || canonical == "" {
		return nil, errors.New("session canonical config directory is required")
	}
	if legacy == "." || legacy == "" || sameConfigDirectory(canonical, legacy) {
		return NewStore(canonical)
	}

	canonicalHasSessions, err := configStoreHasSessions(canonical)
	if err != nil {
		return nil, fmt.Errorf("session: inspect canonical config directory: %w", err)
	}
	legacyHasSessions, err := configStoreHasSessions(legacy)
	if err != nil {
		return nil, fmt.Errorf("session: inspect legacy config directory: %w", err)
	}

	switch {
	case canonicalHasSessions && legacyHasSessions:
		return nil, fmt.Errorf(
			"session: canonical store %q and legacy store %q both contain sessions; set %s to the store to use",
			canonical,
			legacy,
			canonicalConfigEnvironment,
		)
	case legacyHasSessions:
		return NewStore(legacy)
	default:
		return NewStore(canonical)
	}
}

func sameConfigDirectory(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// configStoreHasSessions checks only for named session artifacts. It does not
// create directories, copy files, or combine stores. Session contents remain
// subject to the normal strict validation performed by Load.
func configStoreHasSessions(configDir string) (bool, error) {
	info, err := os.Lstat(configDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %q: %w", configDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%q is a symbolic link", configDir)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%q is not a directory", configDir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return false, fmt.Errorf("%q permissions are too broad (%04o); require 0700", configDir, info.Mode().Perm())
	}

	sessionsDir := filepath.Join(configDir, "sessions")
	info, err = os.Lstat(sessionsDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %q: %w", sessionsDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%q is a symbolic link", sessionsDir)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%q is not a directory", sessionsDir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return false, fmt.Errorf("%q permissions are too broad (%04o); require 0700", sessionsDir, info.Mode().Perm())
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return false, fmt.Errorf("read %q: %w", sessionsDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionFileSuffix) {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), sessionFileSuffix)
		if err := validateName(name); err != nil {
			return false, fmt.Errorf("invalid session filename %q: %w", entry.Name(), err)
		}
		path := filepath.Join(sessionsDir, entry.Name())
		fileInfo, err := os.Lstat(path)
		if err != nil {
			return false, fmt.Errorf("inspect session file %q: %w", path, err)
		}
		if fileInfo.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("session file %q is a symbolic link", path)
		}
		if !fileInfo.Mode().IsRegular() {
			return false, fmt.Errorf("session file %q is not a regular file", path)
		}
		if runtime.GOOS != "windows" && fileInfo.Mode().Perm()&0o077 != 0 {
			return false, fmt.Errorf("session file %q permissions are too broad (%04o); require 0600", path, fileInfo.Mode().Perm())
		}
		return true, nil
	}
	return false, nil
}

// NewStore returns a store rooted at configDir/sessions.
func NewStore(configDir string, options ...StoreOption) (*Store, error) {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		return nil, errors.New("session config directory is required")
	}
	configDir = filepath.Clean(configDir)
	store := &Store{Dir: filepath.Join(configDir, "sessions"), configDir: configDir}
	if err := applyStoreOptions(store, options); err != nil {
		return nil, err
	}
	return store, nil
}

func applyStoreOptions(store *Store, options []StoreOption) error {
	for _, option := range options {
		if option == nil {
			return errors.New("session store option is nil")
		}
		if err := option(store); err != nil {
			return err
		}
	}
	return nil
}

// Path returns the validated path for a named session.
func (store *Store) Path(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	if err := store.validate(); err != nil {
		return "", err
	}
	return filepath.Join(store.Dir, name+sessionFileSuffix), nil
}

// Inspect loads a session and returns only its non-secret summary.
func (store *Store) Inspect(name string) (Summary, error) {
	loaded, err := store.Load(name)
	if err != nil {
		return Summary{}, err
	}
	return loaded.Summary(name), nil
}

// List returns safe summaries in lexical name order.
func (store *Store) List() ([]Summary, error) {
	if err := store.ensureDirectories(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(store.Dir)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	result := make([]Summary, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionFileSuffix) {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), sessionFileSuffix)
		if err := validateName(name); err != nil {
			return nil, fmt.Errorf("list sessions: invalid session filename %q: %w", entry.Name(), err)
		}
		summary, err := store.Inspect(name)
		if err != nil {
			return nil, err
		}
		result = append(result, summary)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// Remove deletes one regular named session file. Symbolic links are rejected.
func (store *Store) Remove(name string) error {
	path, err := store.Path(name)
	if err != nil {
		return err
	}
	if err := store.checkDirectories(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("remove session %q: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("remove session %q: refusing to remove a symbolic link", name)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("remove session %q: path is not a regular file", name)
	}
	if err := store.requireExactSessionName(name); err != nil {
		return err
	}
	var keyID string
	if data, readErr := readPrivateSessionFile(path, name, maxEncryptedSessionFile); readErr == nil {
		if envelope, envelopeErr := decodeEnvelope(data, name); envelopeErr == nil {
			if key, keyErr := store.keyProvider().Get(envelope.KeyID); keyErr == nil {
				if plaintext, openErr := openSession(name, envelope.KeyID, key, envelope.Ciphertext); openErr == nil {
					keyID = envelope.KeyID
					clear(plaintext)
				}
				clear(key)
			}
		}
		clear(data)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove session %q: %w", name, err)
	}
	if err := store.directorySync(store.Dir); err != nil {
		primary := fmt.Errorf("remove session %q: sync session directory: %w", name, err)
		if keyID != "" {
			return errors.Join(primary, fmt.Errorf(
				"remove session %q: key %q was retained because deletion durability was not confirmed; after verifying the file is absent, run `d365-expense session cleanup-key %s`",
				name, keyID, keyID,
			))
		}
		return primary
	}
	if keyID != "" {
		// File deletion remains authoritative, but report partial key cleanup so
		// users know that copied or backed-up ciphertext may remain decryptable.
		if err := store.keyProvider().Delete(keyID); err != nil && !errors.Is(err, errEncryptionKeyNotFound) {
			return fmt.Errorf("remove session %q: session file was removed but keyring entry %q could not be deleted: %w", name, keyID, err)
		}
	}
	return nil
}

func (store *Store) validate() error {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return errors.New("session store directory is required")
	}
	return nil
}

func (store *Store) ensureDirectories() error {
	if err := store.validate(); err != nil {
		return err
	}
	if store.configDir != "" {
		if err := ensurePrivateDirectory(store.configDir); err != nil {
			return fmt.Errorf("session config directory: %w", err)
		}
	}
	if err := ensurePrivateDirectory(store.Dir); err != nil {
		return fmt.Errorf("session store directory: %w", err)
	}
	return nil
}

func (store *Store) checkDirectories() error {
	if err := store.validate(); err != nil {
		return err
	}
	if store.configDir != "" {
		if err := checkPrivateDirectory(store.configDir); err != nil {
			return fmt.Errorf("session config directory: %w", err)
		}
	}
	if err := checkPrivateDirectory(store.Dir); err != nil {
		return fmt.Errorf("session store directory: %w", err)
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create %q: %w", path, err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is a symbolic link", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("restrict %q: %w", path, err)
	}
	return nil
}

func checkPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is a symbolic link", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%q permissions are too broad (%04o); require 0700", path, info.Mode().Perm())
	}
	return nil
}

func openPrivateRegularFile(path, name string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("load session %q: inspect: %w", name, err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("load session %q: refusing to follow a symbolic link", name)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("load session %q: path is not a regular file", name)
	}
	if err := checkPrivateFileMode(path, before); err != nil {
		return nil, fmt.Errorf("load session %q: %w", name, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("load session %q: open: %w", name, err)
	}
	after, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("load session %q: inspect opened file: %w", name, err)
	}
	if !os.SameFile(before, after) {
		file.Close()
		return nil, fmt.Errorf("load session %q: file changed while opening", name)
	}
	if err := checkPrivateFileMode(path, after); err != nil {
		file.Close()
		return nil, fmt.Errorf("load session %q: opened file %w", name, err)
	}
	return file, nil
}

func checkPrivateFileMode(path string, info os.FileInfo) error {
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("permissions for %q are too broad (%04o); require owner-only access", path, info.Mode().Perm())
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

func validateName(name string) error {
	if len(name) == 0 || len(name) > 64 || name == "." || name == ".." {
		return errors.New("session name must be 1-64 ASCII letters, digits, '.', '_', or '-'")
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return fmt.Errorf("session name contains unsupported character %q", character)
	}
	return nil
}

func (store *Store) requireExactSessionName(name string) error {
	entries, err := os.ReadDir(store.Dir)
	if err != nil {
		return fmt.Errorf("resolve session %q filename: %w", name, err)
	}
	target := name + sessionFileSuffix
	for _, entry := range entries {
		if entry.Name() == target {
			return nil
		}
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), target) {
			actual := strings.TrimSuffix(entry.Name(), sessionFileSuffix)
			return fmt.Errorf("session name casing mismatch: requested %q, stored as %q", name, actual)
		}
	}
	return fmt.Errorf("session file for %q disappeared while resolving its exact name", name)
}
