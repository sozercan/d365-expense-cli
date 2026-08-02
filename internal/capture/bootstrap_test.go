package capture_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/capture"
)

func TestParseBootstrapExtractsWorkspaceSessionWithoutDraftFlow(t *testing.T) {
	har := validBootstrapHAR(t)
	profile, err := capture.ParseBootstrap(strings.NewReader(har))
	if err != nil {
		t.Fatalf("ParseBootstrap() error = %v", err)
	}

	if got, want := profile.Session.BaseURL, "https://tenant.operations.dynamics.com"; got != want {
		t.Errorf("BaseURL = %q, want %q", got, want)
	}
	if got, want := profile.Session.ChannelID, int64(7); got != want {
		t.Errorf("ChannelID = %d, want %d", got, want)
	}
	if got, want := profile.Session.LastServerSequence, int64(41); got != want {
		t.Errorf("LastServerSequence = %d, want %d", got, want)
	}
	if got, want := profile.Session.NextClientSequence, int64(101); got != want {
		t.Errorf("NextClientSequence = %d, want %d", got, want)
	}
	wantTarget := capture.CommandTarget{
		CommandName: "Click",
		RootID:      "workspace-root",
		TargetID:    "new-report-target",
		ControlName: "NewExpenseReportReportsTab",
	}
	if got := profile.NewReport; got != wantTarget {
		t.Errorf("NewReport = %#v, want %#v", got, wantTarget)
	}
	if err := profile.Validate(); err != nil {
		t.Errorf("Validate() error = %v", err)
	}

	if _, err := capture.Parse(strings.NewReader(har)); err == nil || !strings.Contains(err.Error(), "draft flow") {
		t.Fatalf("strict Parse() error = %v, want missing complete draft flow", err)
	}
}

func TestLoadBootstrapReadsHARFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.har")
	if err := os.WriteFile(path, []byte(validBootstrapHAR(t)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	profile, err := capture.LoadBootstrap(path)
	if err != nil {
		t.Fatalf("LoadBootstrap() error = %v", err)
	}
	if got, want := profile.NewReport.ControlName, "NewExpenseReportReportsTab"; got != want {
		t.Errorf("ControlName = %q, want %q", got, want)
	}
}

func TestParseBootstrapSelectsLatestContiguousSessionAfterInitialization(t *testing.T) {
	profile, err := capture.ParseBootstrap(strings.NewReader(bootstrapHARWithInitializationGroups(t)))
	if err != nil {
		t.Fatalf("ParseBootstrap() error = %v", err)
	}

	if got, want := profile.Session.ChannelID, int64(7); got != want {
		t.Errorf("ChannelID = %d, want final workspace channel %d", got, want)
	}
	if got, want := profile.Session.RequestHeaders.Get("ms-dyn-bsid"), "bootstrap-header-secret"; got != want {
		t.Errorf("ms-dyn-bsid = %q, want current workspace credential", got)
	}
	if got, want := profile.NewReport.RootID, "workspace-root"; got != want {
		t.Errorf("RootID = %q, want %q", got, want)
	}
}

func TestParseBootstrapDoesNotFallBackPastTrailingSession(t *testing.T) {
	har := mutateHAR(t, validBootstrapHAR(t), func(document map[string]any) {
		entries := harEntries(document)
		entries = append(entries, bootstrapInitializationEntry(8, 50, 200, 51, "trailing-header", "trailing-cookie"))
		document["log"].(map[string]any)["entries"] = entries
	})

	_, err := capture.ParseBootstrap(strings.NewReader(har))
	if err == nil || !strings.Contains(err.Error(), "active ExpenseWorkspace_form") {
		t.Fatalf("ParseBootstrap() error = %v, want unusable trailing session error", err)
	}
}

func TestParseBootstrapRejectsMultipleActiveWorkspaceRoots(t *testing.T) {
	har := mutateHAR(t, validBootstrapHAR(t), func(document map[string]any) {
		mutateEntryResponseEnvelope(t, document, 0, func(body map[string]any) {
			interactions := body["Messages"].([]any)[0].(map[string]any)["Interactions"].([]any)
			interactions = append(interactions, map[string]any{
				"$type":  "CreateViewModelInteraction",
				"RootId": "other-workspace-root",
				"Descriptor": map[string]any{
					"Id":   "other-workspace-root",
					"Name": "ExpenseWorkspace_form",
					"ChildViewModels": []any{
						map[string]any{"Id": "other-new-report-target", "Name": "NewExpenseReportReportsTab"},
					},
				},
			})
			body["Messages"].([]any)[0].(map[string]any)["Interactions"] = interactions
		})
	})

	_, err := capture.ParseBootstrap(strings.NewReader(har))
	if err == nil || !strings.Contains(err.Error(), "multiple active ExpenseWorkspace_form") {
		t.Fatalf("ParseBootstrap() error = %v, want multiple active workspace error", err)
	}
}

func TestParseBootstrapRejectsMultipleNewReportControls(t *testing.T) {
	har := mutateHAR(t, validBootstrapHAR(t), func(document map[string]any) {
		mutateEntryResponseEnvelope(t, document, 0, func(body map[string]any) {
			descriptor := body["Messages"].([]any)[0].(map[string]any)["Interactions"].([]any)[0].(map[string]any)["Descriptor"].(map[string]any)
			children := descriptor["ChildViewModels"].([]any)
			children = append(children, map[string]any{"Id": "other-new-report-target", "Name": "NewExpenseReportReportsTab"})
			descriptor["ChildViewModels"] = children
		})
	})

	_, err := capture.ParseBootstrap(strings.NewReader(har))
	if err == nil || !strings.Contains(err.Error(), "exactly one NewExpenseReportReportsTab") {
		t.Fatalf("ParseBootstrap() error = %v, want ambiguous new-report control error", err)
	}
}

func TestParseBootstrapRejectsDeletedWorkspaceRoot(t *testing.T) {
	har := mutateHAR(t, validBootstrapHAR(t), func(document map[string]any) {
		mutateEntryResponseEnvelope(t, document, 0, func(body map[string]any) {
			interactions := body["Messages"].([]any)[0].(map[string]any)["Interactions"].([]any)
			interactions = append(interactions, map[string]any{
				"$type":    "DeleteViewModelInteraction",
				"TargetId": "workspace-root",
			})
			body["Messages"].([]any)[0].(map[string]any)["Interactions"] = interactions
		})
	})

	_, err := capture.ParseBootstrap(strings.NewReader(har))
	if err == nil || !strings.Contains(err.Error(), "active ExpenseWorkspace_form") {
		t.Fatalf("ParseBootstrap() error = %v, want deleted workspace error", err)
	}
}

func TestBootstrapSafeSummaryOmitsCredentialValuesAndQuery(t *testing.T) {
	profile, err := capture.ParseBootstrap(strings.NewReader(validBootstrapHAR(t)))
	if err != nil {
		t.Fatal(err)
	}
	summary := profile.SafeSummary()
	for _, secret := range []string{"bootstrap-header-secret", "bootstrap-cookie-secret", "cmp=USMF", "lng=en-us"} {
		if strings.Contains(summary, secret) {
			t.Fatalf("SafeSummary() leaked %q: %s", secret, summary)
		}
	}
	for _, name := range []string{"ms-dyn-bsid", "DynamicsOwinAuth", "NewExpenseReportReportsTab"} {
		if !strings.Contains(strings.ToLower(summary), strings.ToLower(name)) {
			t.Errorf("SafeSummary() = %q, want name %q", summary, name)
		}
	}
}
