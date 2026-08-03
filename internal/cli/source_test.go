package cli

import (
	"errors"
	"net/http"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/expense"
	sessionstore "github.com/sozercan/d365-expense-cli/internal/session"
)

func TestNamedSessionExecutionCheckpointsStatus(t *testing.T) {
	config := t.TempDir()
	t.Setenv("MSEXPENSE_CONFIG_DIR", config)
	store, err := sessionstore.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
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
