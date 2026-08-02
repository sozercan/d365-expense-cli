package session

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestSessionLockRejectsConcurrentUseAndCanBeReacquired(t *testing.T) {
	store := newTestStore(t)
	first, err := store.AcquireLock("work")
	if err != nil {
		t.Fatalf("AcquireLock() error = %v", err)
	}
	lockPath := filepath.Join(store.Dir, ".work.lock")
	if runtime.GOOS != "windows" {
		info, err := os.Stat(lockPath)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("lock mode = %v %04o, want directory 0700", info.Mode().Type(), info.Mode().Perm())
		}
	}

	secondStore, err := NewStore(store.configDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secondStore.Lock("work"); !errors.Is(err, ErrLocked) || !errors.Is(err, ErrSessionLocked) {
		t.Fatalf("concurrent Lock() error = %v, want ErrLocked", err)
	}

	other, err := store.Lock("other")
	if err != nil {
		t.Fatalf("Lock(other) error = %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatalf("Close(other) error = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}
	if _, err := os.Lstat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock still exists after release: %v", err)
	}

	reacquired, err := secondStore.AcquireLock("work")
	if err != nil {
		t.Fatalf("reacquire error = %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatalf("reacquired Close() error = %v", err)
	}
}

func TestSessionLockIsAtomicAcrossConcurrentCallers(t *testing.T) {
	store := newTestStore(t)
	const callers = 16
	start := make(chan struct{})
	results := make(chan *Lock, callers)
	errorsCh := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			lock, err := store.AcquireLock("shared")
			if err != nil {
				errorsCh <- err
				return
			}
			results <- lock
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsCh)

	locks := make([]*Lock, 0)
	for lock := range results {
		locks = append(locks, lock)
	}
	if len(locks) != 1 {
		t.Fatalf("successful locks = %d, want 1", len(locks))
	}
	failures := 0
	for err := range errorsCh {
		failures++
		if !errors.Is(err, ErrLocked) {
			t.Errorf("concurrent error = %v, want ErrLocked", err)
		}
	}
	if failures != callers-1 {
		t.Fatalf("lock failures = %d, want %d", failures, callers-1)
	}
	if err := locks[0].Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLockValidatesNameAndStore(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.AcquireLock("bad/name"); err == nil {
		t.Fatal("AcquireLock(bad name) error = nil")
	}
	var nilStore *Store
	if _, err := nilStore.AcquireLock("work"); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("nil store AcquireLock() error = %v", err)
	}
}

func TestSessionLockReleaseDetectsTampering(t *testing.T) {
	store := newTestStore(t)
	lock, err := store.AcquireLock("work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lock.path); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err == nil || !strings.Contains(err.Error(), "disappeared") {
		t.Fatalf("Release() error = %v, want disappeared lock error", err)
	}
}

func TestListIgnoresActiveLockDirectories(t *testing.T) {
	store := newTestStore(t)
	if err := store.Save("work", validSession(t)); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireLock("work")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	listed, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != "work" {
		t.Fatalf("List() = %+v", listed)
	}
}

func TestBreakLockRemovesOnlyEmptyOwnedLock(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireLock("work")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BreakLock("work"); err != nil {
		t.Fatalf("BreakLock() error = %v", err)
	}
	if err := lock.Release(); err == nil {
		t.Fatal("Release() after BreakLock error = nil")
	}
	lock, err = store.AcquireLock("work")
	if err != nil {
		t.Fatalf("AcquireLock() after BreakLock error = %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}
