package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/expense"
	sessionstore "github.com/sozercan/d365-expense-cli/internal/session"
)

func TestNamedSessionExecutionCheckpointsStatus(t *testing.T) {
	config := t.TempDir()
	store, err := sessionstore.NewStore(config, sessionstore.WithKeyProvider(newSourceTestKeyProvider()))
	if err != nil {
		t.Fatal(err)
	}
	previousStoreFactory := defaultSessionStore
	defaultSessionStore = func() (*sessionstore.Store, error) { return store, nil }
	t.Cleanup(func() { defaultSessionStore = previousStoreFactory })
	stored, err := sessionstore.FromBootstrap(testBootstrapProfile())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("work", stored); err != nil {
		t.Fatal(err)
	}

	execution, err := beginNamedSessionExecution("work")
	if err != nil {
		t.Fatal(err)
	}
	inProgress, err := store.Load("work")
	if err != nil {
		t.Fatal(err)
	}
	if inProgress.Status != sessionstore.StatusInProgress {
		t.Fatalf("status = %q, want in_progress", inProgress.Status)
	}
	if err := execution.finish(nil); err != nil {
		t.Fatal(err)
	}
	ready, err := store.Load("work")
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != sessionstore.StatusReady {
		t.Fatalf("status = %q, want ready", ready.Status)
	}

	execution, err = beginNamedSessionExecution("work")
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.finish(errors.New("remote operation failed")); err != nil {
		t.Fatal(err)
	}
	uncertain, err := store.Load("work")
	if err != nil {
		t.Fatal(err)
	}
	if uncertain.Status != sessionstore.StatusUncertain {
		t.Fatalf("status = %q, want uncertain", uncertain.Status)
	}
	if _, err := beginNamedSessionExecution("work"); err == nil {
		t.Fatal("uncertain session was accepted for execution")
	}

	lateAuth, err := sessionstore.FromBootstrap(testBootstrapProfile())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("late-auth", lateAuth); err != nil {
		t.Fatal(err)
	}
	execution, err = beginNamedSessionExecution("late-auth")
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.finish(errors.Join(expense.ErrOperationUncertain, expense.ErrAuthenticationExpired)); err != nil {
		t.Fatal(err)
	}
	lateAuth, err = store.Load("late-auth")
	if err != nil {
		t.Fatal(err)
	}
	if lateAuth.Status != sessionstore.StatusUncertain {
		t.Fatalf("late-auth status = %q, want uncertain", lateAuth.Status)
	}

	earlyAuth, err := sessionstore.FromBootstrap(testBootstrapProfile())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("early-auth", earlyAuth); err != nil {
		t.Fatal(err)
	}
	execution, err = beginNamedSessionExecution("early-auth")
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.finish(expense.ErrAuthenticationExpired); err != nil {
		t.Fatal(err)
	}
	earlyAuth, err = store.Load("early-auth")
	if err != nil {
		t.Fatal(err)
	}
	if earlyAuth.Status != sessionstore.StatusExpired {
		t.Fatalf("early-auth status = %q, want expired", earlyAuth.Status)
	}
}

func TestBeginNamedSessionExecutionMigratesLegacyPlaintextBeforeNetwork(t *testing.T) {
	config := t.TempDir()
	store, err := sessionstore.NewStore(config, sessionstore.WithKeyProvider(newSourceTestKeyProvider()))
	if err != nil {
		t.Fatal(err)
	}
	previousStoreFactory := defaultSessionStore
	defaultSessionStore = func() (*sessionstore.Store, error) { return store, nil }
	t.Cleanup(func() { defaultSessionStore = previousStoreFactory })

	stored, err := sessionstore.FromBootstrap(testBootstrapProfile())
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.Path("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	plaintext, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	plaintext = append(plaintext, '\n')
	if err := os.WriteFile(path, plaintext, 0o600); err != nil {
		t.Fatal(err)
	}

	execution, err := beginNamedSessionExecution("legacy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = execution.lock.Release() })
	checkpoint, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(bytes.TrimSpace(checkpoint), []byte("{")) {
		t.Fatal("pre-network checkpoint remained plaintext JSON")
	}
	for _, secret := range []string{"header-secret", "cookie-secret", "new-report"} {
		if bytes.Contains(checkpoint, []byte(secret)) {
			t.Fatalf("encrypted checkpoint leaked %q", secret)
		}
	}
	loaded, err := store.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != sessionstore.StatusInProgress {
		t.Fatalf("status = %q, want in_progress", loaded.Status)
	}
}

type sourceTestKeyProvider struct {
	mu   sync.Mutex
	keys map[string][]byte
}

func newSourceTestKeyProvider() *sourceTestKeyProvider {
	return &sourceTestKeyProvider{keys: make(map[string][]byte)}
}

func (provider *sourceTestKeyProvider) Get(id string) ([]byte, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	key, ok := provider.keys[id]
	if !ok {
		return nil, errors.New("missing test key")
	}
	return append([]byte(nil), key...), nil
}

func (provider *sourceTestKeyProvider) Set(id string, key []byte) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.keys[id] = append([]byte(nil), key...)
	return nil
}

func (provider *sourceTestKeyProvider) Delete(id string) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	delete(provider.keys, id)
	return nil
}

func testBootstrapProfile() *capture.BootstrapProfile {
	headers := make(http.Header)
	for _, name := range []string{"ms-dyn-aid", "ms-dyn-bsid", "ms-dyn-csrftoken", "ms-dyn-sid"} {
		headers.Set(name, "header-secret")
	}
	headers.Set("Origin", "https://tenant.operations.dynamics.com")
	headers.Set("Referer", "https://tenant.operations.dynamics.com/?cmp=USMF")
	headers.Set("Content-Type", "application/json; charset=UTF-8")
	return &capture.BootstrapProfile{
		Session: capture.SessionProfile{
			BaseURL:            "https://tenant.operations.dynamics.com",
			EndpointURL:        "https://tenant.operations.dynamics.com/Services/ReliableCommunicationManager.svc/ProcessMessages?cmp=USMF&lng=en-us",
			RequestHeaders:     headers,
			Cookies:            []*http.Cookie{{Name: "DynamicsOwinAuth", Value: "cookie-secret"}, {Name: "ms-dyn-csrftoken", Value: "cookie-secret"}},
			Company:            "USMF",
			Language:           "en-us",
			ChannelID:          7,
			LastServerSequence: 41,
			NextClientSequence: 101,
		},
		NewReport: capture.CommandTarget{CommandName: "Click", RootID: "workspace-root", TargetID: "new-report", ControlName: "NewExpenseReportReportsTab"},
	}
}
