package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// ErrLocked reports that another process or goroutine already owns the named
// session lock.
var ErrLocked = errors.New("session is already in use")

// ErrSessionLocked is an explicit alias suitable for errors.Is checks.
var ErrSessionLocked = ErrLocked

// Lock is an exclusive lock represented by an atomically created directory.
// It is portable and does not depend on advisory OS-specific file locking.
type Lock struct {
	path string
	name string

	mu       sync.Mutex
	released bool
}

// AcquireLock atomically acquires exclusive use of a named session.
func (store *Store) AcquireLock(name string) (*Lock, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	if err := store.ensureDirectories(); err != nil {
		return nil, err
	}
	path := filepath.Join(store.Dir, "."+name+".lock")
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("lock session %q: %w", name, ErrLocked)
		}
		return nil, fmt.Errorf("lock session %q: %w", name, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("lock session %q: restrict lock directory: %w", name, err)
	}
	return &Lock{path: path, name: name}, nil
}

// Lock is a concise alias for AcquireLock.
func (store *Store) Lock(name string) (*Lock, error) {
	return store.AcquireLock(name)
}

// Release relinquishes the lock. Repeated releases are harmless.
func (lock *Lock) Release() error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.released {
		return nil
	}
	info, err := os.Lstat(lock.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("unlock session %q: lock directory disappeared", lock.name)
		}
		return fmt.Errorf("unlock session %q: inspect lock directory: %w", lock.name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("unlock session %q: lock path is not the owned directory", lock.name)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("unlock session %q: lock permissions are too broad", lock.name)
	}
	if err := os.Remove(lock.path); err != nil {
		return fmt.Errorf("unlock session %q: %w", lock.name, err)
	}
	lock.released = true
	return nil
}

// Close implements io.Closer and delegates to Release.
func (lock *Lock) Close() error { return lock.Release() }

// BreakLock removes a stale named lock directory. Callers must ensure no
// process is actively using the session before invoking this recovery action.
func (store *Store) BreakLock(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := store.checkDirectories(); err != nil {
		return err
	}
	path := filepath.Join(store.Dir, "."+name+".lock")
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("break lock for session %q: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("break lock for session %q: lock path is not a directory", name)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("break lock for session %q: lock permissions are too broad", name)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("break lock for session %q: inspect lock directory: %w", name, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("break lock for session %q: lock directory is not empty", name)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("break lock for session %q: %w", name, err)
	}
	return nil
}
