package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefaultStoreCanonicalEnvironmentTakesPrecedence(t *testing.T) {
	canonical := filepath.Join(t.TempDir(), "canonical-config")
	legacy := filepath.Join(t.TempDir(), "legacy-config")
	t.Setenv(canonicalConfigEnvironment, "  "+canonical+"  ")
	t.Setenv(legacyConfigEnvironment, legacy)
	store, err := DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore() error = %v", err)
	}
	if got, want := store.Dir, filepath.Join(canonical, "sessions"); got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
	path, err := store.Path("work")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := path, filepath.Join(canonical, "sessions", "work.session.json"); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestDefaultStoreUsesLegacyEnvironmentWhenCanonicalUnset(t *testing.T) {
	legacy := filepath.Join(t.TempDir(), "legacy-config")
	t.Setenv(canonicalConfigEnvironment, "")
	t.Setenv(legacyConfigEnvironment, "  "+legacy+"  ")
	store, err := DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore() error = %v", err)
	}
	if got, want := store.Dir, filepath.Join(legacy, "sessions"); got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

func TestResolveDefaultStoreUsesUserConfigDirectory(t *testing.T) {
	root := t.TempDir()
	userConfig := filepath.Join(root, "config")
	home := filepath.Join(root, "home")
	store, err := resolveDefaultStore(
		func(string) string { return "" },
		func() (string, error) { return userConfig, nil },
		func() (string, error) { return home, nil },
	)
	if err != nil {
		t.Fatalf("resolveDefaultStore() error = %v", err)
	}
	store.keys = newMemoryKeyProvider()
	if got, want := store.Dir, filepath.Join(userConfig, canonicalConfigDirectory, "sessions"); got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
	if err := store.Save("work", validSession(t)); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{store.configDir, store.Dir} {
			info, statErr := os.Stat(path)
			if statErr != nil {
				t.Fatal(statErr)
			}
			if got := info.Mode().Perm(); got != 0o700 {
				t.Errorf("directory %q mode = %04o, want 0700", path, got)
			}
		}
	}
}

func TestDefaultStoreForRootsFallsBackToExistingLegacySessions(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.Join(root, "config", canonicalConfigDirectory)
	legacy := filepath.Join(root, "home", legacyConfigDirectory)
	provider := newMemoryKeyProvider()
	legacyStore, err := NewStore(legacy, WithKeyProvider(provider))
	if err != nil {
		t.Fatal(err)
	}
	wantSession := validSession(t)
	if err := legacyStore.Save("work", wantSession); err != nil {
		t.Fatal(err)
	}

	store, err := defaultStoreForRoots(canonical, legacy)
	if err != nil {
		t.Fatalf("defaultStoreForRoots() error = %v", err)
	}
	if store.configDir != legacy {
		t.Fatalf("configDir = %q, want legacy %q", store.configDir, legacy)
	}
	store.keys = provider
	if _, err := os.Lstat(canonical); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy fallback unexpectedly created or migrated canonical store: %v", err)
	}
	loaded, err := store.Load("work")
	if err != nil {
		t.Fatalf("Load() legacy schema error = %v", err)
	}
	if !reflect.DeepEqual(loaded, wantSession) {
		t.Fatalf("legacy schema changed during fallback:\n got: %#v\nwant: %#v", loaded, wantSession)
	}
}

func TestDefaultStoreForRootsPrefersCanonicalWhenOnlyCanonicalHasSessions(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.Join(root, "config", canonicalConfigDirectory)
	legacy := filepath.Join(root, "home", legacyConfigDirectory)
	canonicalStore, err := NewStore(canonical, WithKeyProvider(newMemoryKeyProvider()))
	if err != nil {
		t.Fatal(err)
	}
	if err := canonicalStore.Save("work", validSession(t)); err != nil {
		t.Fatal(err)
	}

	store, err := defaultStoreForRoots(canonical, legacy)
	if err != nil {
		t.Fatalf("defaultStoreForRoots() error = %v", err)
	}
	if store.configDir != canonical {
		t.Fatalf("configDir = %q, want canonical %q", store.configDir, canonical)
	}
}

func TestDefaultStoreForRootsRejectsConflictingSessionStores(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.Join(root, "config", canonicalConfigDirectory)
	legacy := filepath.Join(root, "home", legacyConfigDirectory)
	for _, configDir := range []string{canonical, legacy} {
		store, err := NewStore(configDir, WithKeyProvider(newMemoryKeyProvider()))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save("work", validSession(t)); err != nil {
			t.Fatal(err)
		}
	}

	_, err := defaultStoreForRoots(canonical, legacy)
	if err == nil || !strings.Contains(err.Error(), "both contain sessions") || !strings.Contains(err.Error(), canonicalConfigEnvironment) {
		t.Fatalf("defaultStoreForRoots() error = %v, want explicit conflicting-store error", err)
	}
}

func TestDefaultStoreForRootsDoesNotTreatUnrelatedLegacyFilesAsSessions(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.Join(root, "config", canonicalConfigDirectory)
	legacy := filepath.Join(root, "home", legacyConfigDirectory)
	if err := os.MkdirAll(filepath.Join(legacy, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "sessions", "README.txt"), []byte("not a session"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := defaultStoreForRoots(canonical, legacy)
	if err != nil {
		t.Fatalf("defaultStoreForRoots() error = %v", err)
	}
	if store.configDir != canonical {
		t.Fatalf("configDir = %q, want canonical %q", store.configDir, canonical)
	}
}

func TestDefaultStoreForRootsRejectsUnsafeLegacySessionFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	root := t.TempDir()
	canonical := filepath.Join(root, "config", canonicalConfigDirectory)
	legacy := filepath.Join(root, "home", legacyConfigDirectory)
	legacyStore, err := NewStore(legacy, WithKeyProvider(newMemoryKeyProvider()))
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyStore.Save("work", validSession(t)); err != nil {
		t.Fatal(err)
	}
	path, err := legacyStore.Path("work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = defaultStoreForRoots(canonical, legacy)
	if err == nil || !strings.Contains(err.Error(), "require 0600") {
		t.Fatalf("defaultStoreForRoots() error = %v, want unsafe legacy session rejection", err)
	}
}

func TestNewStoreRejectsBlankConfigDirectory(t *testing.T) {
	if _, err := NewStore(" \t "); err == nil {
		t.Fatal("NewStore(blank) error = nil")
	}
}

func TestSessionNameValidationAndFileSuffix(t *testing.T) {
	store := &Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	valid := []string{"a", "Work_01", "finance.us-west", strings.Repeat("a", 64)}
	for _, name := range valid {
		path, err := store.Path(name)
		if err != nil {
			t.Errorf("Path(%q) error = %v", name, err)
			continue
		}
		if filepath.Base(path) != name+sessionFileSuffix {
			t.Errorf("Path(%q) base = %q", name, filepath.Base(path))
		}
	}
	invalid := []string{"", ".", "..", "a/b", "a\\b", "has space", "é", strings.Repeat("a", 65)}
	for _, name := range invalid {
		if _, err := store.Path(name); err == nil {
			t.Errorf("Path(%q) error = nil", name)
		}
	}
}

func TestStoreOwnerOnlyRoundTripInspectListAndRemove(t *testing.T) {
	store := newTestStore(t)
	alpha := validSession(t)
	zulu := cloneSession(t, alpha)
	zulu.NextClientSequence++
	if err := store.Save("zulu", zulu); err != nil {
		t.Fatalf("Save(zulu) error = %v", err)
	}
	if err := store.Save("alpha", alpha); err != nil {
		t.Fatalf("Save(alpha) error = %v", err)
	}
	alphaPath, _ := store.Path("alpha")
	alphaBytes, err := os.ReadFile(alphaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(alphaBytes), encryptedSessionMagic) {
		t.Fatalf("session file does not use encrypted envelope")
	}
	for _, secret := range []string{"header-secret", "cookie-secret", "csrf-cookie-secret", "new-report-target", "cmp=USMF"} {
		if bytes.Contains(alphaBytes, []byte(secret)) {
			t.Fatalf("encrypted session file leaked %q", secret)
		}
	}

	if runtime.GOOS != "windows" {
		for _, path := range []string{store.configDir, store.Dir} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o700 {
				t.Errorf("directory %q mode = %04o, want 0700", path, got)
			}
		}
		path, _ := store.Path("alpha")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("session mode = %04o, want 0600", got)
		}
	}

	loaded, err := store.Load("alpha")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(loaded, alpha) {
		t.Fatalf("round trip mismatch:\n got: %#v\nwant: %#v", loaded, alpha)
	}
	inspect, err := store.Inspect("alpha")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if inspect.Name != "alpha" || inspect.NextClientSequence != alpha.NextClientSequence {
		t.Fatalf("Inspect() = %+v", inspect)
	}
	serialized, err := json.Marshal(inspect)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"header-secret", "cookie-secret", "csrf-cookie-secret"} {
		if strings.Contains(string(serialized), secret) || strings.Contains(inspect.String(), secret) {
			t.Fatalf("summary leaked %q", secret)
		}
	}

	if err := os.WriteFile(filepath.Join(store.Dir, "ignore.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir, ".d365-expense-session-leftover"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := summaryNames(listed), []string{"alpha", "zulu"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("List() names = %v, want %v", got, want)
	}

	if err := store.Remove("alpha"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := store.Load("alpha"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load() after Remove error = %v, want os.ErrNotExist", err)
	}
	if err := store.Remove("alpha"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second Remove() error = %v, want os.ErrNotExist", err)
	}
}

func TestStoreAtomicallyReplacesAndLeavesNoTemporaryFiles(t *testing.T) {
	store := newTestStore(t)
	first := validSession(t)
	if err := store.Save("work", first); err != nil {
		t.Fatal(err)
	}
	second := cloneSession(t, first)
	second.LastServerSequence = 500
	second.NextClientSequence = 600
	if err := store.Save("work", second); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("work")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastServerSequence != 500 || loaded.NextClientSequence != 600 {
		t.Fatalf("loaded sequence = %d/%d", loaded.LastServerSequence, loaded.NextClientSequence)
	}
	entries, err := os.ReadDir(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".d365-expense-session-") {
			t.Errorf("temporary file was not removed: %s", entry.Name())
		}
	}
	if runtime.GOOS != "windows" {
		path, _ := store.Path("work")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("replacement mode = %04o, want 0600", info.Mode().Perm())
		}
	}
}

func TestLoadRejectsUnknownFieldsTrailingDataAndUnsupportedVersion(t *testing.T) {
	store := newTestStore(t)
	if err := store.ensureDirectories(); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("work")
	original, err := encodeSession(validSession(t))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func([]byte) []byte
		want   string
	}{
		{
			name: "top-level unknown field",
			mutate: func(data []byte) []byte {
				var document map[string]any
				mustUnmarshal(t, data, &document)
				document["unexpected"] = true
				return mustMarshal(t, document)
			},
			want: "unknown field",
		},
		{
			name: "nested header unknown field",
			mutate: func(data []byte) []byte {
				var document map[string]any
				mustUnmarshal(t, data, &document)
				document["headers"].([]any)[0].(map[string]any)["unexpected"] = true
				return mustMarshal(t, document)
			},
			want: "unknown field",
		},
		{
			name: "nested cookie unknown field",
			mutate: func(data []byte) []byte {
				var document map[string]any
				mustUnmarshal(t, data, &document)
				document["cookies"].([]any)[0].(map[string]any)["unexpected"] = true
				return mustMarshal(t, document)
			},
			want: "unknown field",
		},
		{
			name:   "trailing value",
			mutate: func(data []byte) []byte { return append(append([]byte(nil), data...), []byte("\n{}")...) },
			want:   "trailing JSON",
		},
		{
			name: "unsupported version",
			mutate: func(data []byte) []byte {
				var document map[string]any
				mustUnmarshal(t, data, &document)
				document["version"] = float64(Version + 1)
				return mustMarshal(t, document)
			},
			want: "version",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, test.mutate(original), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := store.Load("work")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestLoadRejectsBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	store := newTestStore(t)
	if err := store.Save("work", validSession(t)); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("work")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("work"); err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("Load() error = %v, want broad permission rejection", err)
	}
}

func TestLoadRejectsBroadDirectoryPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	store := newTestStore(t)
	if err := store.Save("work", validSession(t)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("work"); err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("Load() error = %v, want broad directory permission rejection", err)
	}
}

func TestStoreRejectsSessionFileSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation can require elevated privileges on Windows")
	}
	store := newTestStore(t)
	if err := store.Save("real", validSession(t)); err != nil {
		t.Fatal(err)
	}
	realPath, _ := store.Path("real")
	linkPath, _ := store.Path("alias")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("alias"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Load(symlink) error = %v", err)
	}
	if err := store.Save("alias", validSession(t)); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Save(symlink) error = %v", err)
	}
	if err := store.Remove("alias"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Remove(symlink) error = %v", err)
	}
}

func TestStoreRejectsSessionDirectorySymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation can require elevated privileges on Windows")
	}
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(root, "sessions")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	store := &Store{Dir: linkDir}
	if err := store.Save("work", validSession(t)); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Save() error = %v, want directory symlink rejection", err)
	}
}

func TestLoadRejectsOversizedSessionFile(t *testing.T) {
	store := newTestStore(t)
	if err := store.ensureDirectories(); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("large")
	if err := os.WriteFile(path, make([]byte, maxEncryptedSessionFile+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("large"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Load() error = %v, want size rejection", err)
	}
}

func TestListRejectsMalformedOwnedSessionFilename(t *testing.T) {
	store := newTestStore(t)
	if err := store.ensureDirectories(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir, "é.session.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil || !strings.Contains(err.Error(), "invalid session filename") {
		t.Fatalf("List() error = %v", err)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "config"), WithKeyProvider(newMemoryKeyProvider()))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func validSession(t *testing.T) *Session {
	t.Helper()
	result, err := fromBootstrapAt(validBootstrapProfile(), fixedTestTime())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func fixedTestTime() (result time.Time) {
	return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
}

func summaryNames(summaries []Summary) []string {
	result := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		result = append(result, summary.Name)
	}
	return result
}

func mustUnmarshal(t *testing.T, data []byte, destination any) {
	t.Helper()
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatal(err)
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
