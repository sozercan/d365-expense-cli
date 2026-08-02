package capture_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/capture"
)

func TestParseExtractsExecutableSessionAndDraftFlow(t *testing.T) {
	profile, err := capture.Parse(strings.NewReader(validHAR(t)))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	session := profile.Session
	if got, want := session.BaseURL, "https://tenant.operations.dynamics.com"; got != want {
		t.Errorf("BaseURL = %q, want %q", got, want)
	}
	if got, want := session.EndpointURL, "https://tenant.operations.dynamics.com/Services/ReliableCommunicationManager.svc/ProcessMessages?cmp=USMF&lng=en-us"; got != want {
		t.Errorf("EndpointURL = %q, want %q", got, want)
	}
	if got, want := session.Company, "USMF"; got != want {
		t.Errorf("Company = %q, want %q", got, want)
	}
	if got, want := session.Language, "en-us"; got != want {
		t.Errorf("Language = %q, want %q", got, want)
	}
	if got, want := session.ChannelID, int64(7); got != want {
		t.Errorf("ChannelID = %d, want %d", got, want)
	}
	if got, want := session.LastServerSequence, int64(43); got != want {
		t.Errorf("LastServerSequence = %d, want %d", got, want)
	}
	if got, want := session.NextClientSequence, int64(104); got != want {
		t.Errorf("NextClientSequence = %d, want %d", got, want)
	}

	for _, name := range []string{"Accept", "Content-Type", "Origin", "Referer", "X-Requested-With", "ms-dyn-aid", "ms-dyn-bsid", "ms-dyn-csrftoken", "ms-dyn-sid"} {
		if session.RequestHeaders.Get(name) == "" {
			t.Errorf("RequestHeaders.Get(%q) is empty", name)
		}
	}
	for _, name := range []string{"Cookie", "Host", "Content-Length", "Connection", "Accept-Encoding", "Sec-Fetch-Mode", "sec-ch-ua"} {
		if got := session.RequestHeaders.Get(name); got != "" {
			t.Errorf("RequestHeaders unexpectedly retained %q = %q", name, got)
		}
	}
	if got, want := cookieNames(session.Cookies), []string{"DynamicsOwinAuth", "backend-affinity", "ms-dyn-csrftoken"}; !reflect.DeepEqual(got, want) {
		t.Errorf("cookie names = %v, want %v", got, want)
	}

	wantNew := capture.CommandTarget{
		CommandName: "Click",
		RootID:      "workspace-root",
		TargetID:    "new-report-target",
		ControlName: "NewExpenseReportReportsTab",
	}
	if got := profile.Draft.NewReport; got != wantNew {
		t.Errorf("NewReport = %#v, want %#v", got, wantNew)
	}
	wantSet := capture.CommandTarget{
		CommandName: "SetValue",
		RootID:      "new-report-root",
		TargetID:    "purpose-target",
		ControlName: "NamePurpose",
	}
	if got := profile.Draft.CreateDraft.SetValue; got != wantSet {
		t.Errorf("SetValue = %#v, want %#v", got, wantSet)
	}
	wantDefault := capture.CommandTarget{
		CommandName: "ExecuteShortcuts",
		RootID:      "new-report-root",
		TargetID:    "new-report-root",
		ControlName: "ExpenseNewExpenseReport_form",
	}
	if got := profile.Draft.CreateDraft.InvokeDefaultButton; got != wantDefault {
		t.Errorf("InvokeDefaultButton = %#v, want %#v", got, wantDefault)
	}
	wantSave := capture.CommandTarget{
		CommandName: "Click",
		RootID:      "details-root",
		TargetID:    "save-target",
		ControlName: "SaveAndClose",
	}
	if got := profile.Draft.SaveAndClose; got != wantSave {
		t.Errorf("SaveAndClose = %#v, want %#v", got, wantSave)
	}

	if err := profile.Validate(); err != nil {
		t.Errorf("Validate() error = %v", err)
	}
}

func TestParseRejectsRedactedCredentials(t *testing.T) {
	har := validHARWith(t, fixtureOptions{headerValue: "<redacted>"})

	_, err := capture.Parse(strings.NewReader(har))
	if !errors.Is(err, capture.ErrCredentials) {
		t.Fatalf("Parse() error = %v, want errors.Is(ErrCredentials)", err)
	}
	var credentialErr *capture.CredentialError
	if !errors.As(err, &credentialErr) {
		t.Fatalf("Parse() error type = %T, want *CredentialError", err)
	}
	if got, want := credentialErr.RedactedHeaders, []string{"ms-dyn-aid", "ms-dyn-bsid", "ms-dyn-csrftoken", "ms-dyn-sid"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RedactedHeaders = %v, want %v", got, want)
	}
}

func cookieNames(cookies []*http.Cookie) []string {
	names := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		names = append(names, cookie.Name)
	}
	return names
}

func TestSafeSummaryOmitsCredentialValuesAndURLQuery(t *testing.T) {
	profile, err := capture.Parse(strings.NewReader(validHAR(t)))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	summary := profile.SafeSummary()
	for _, secret := range []string{"captured-header-secret", "captured-cookie-secret", "?cmp=USMF", "lng=en-us"} {
		if strings.Contains(summary, secret) {
			t.Errorf("SafeSummary() contains secret/query value %q: %s", secret, summary)
		}
	}
	for _, name := range []string{"Ms-Dyn-Csrftoken", "DynamicsOwinAuth", "NamePurpose", "SaveAndClose"} {
		if !strings.Contains(summary, name) {
			t.Errorf("SafeSummary() does not contain safe name %q: %s", name, summary)
		}
	}
}

func TestParseRejectsMissingCredentials(t *testing.T) {
	har := mutateHAR(t, validHAR(t), func(document map[string]any) {
		forEachProcessRequest(document, func(request map[string]any) {
			request["headers"] = filterNamedValues(request["headers"].([]any), func(name string) bool {
				return !strings.EqualFold(name, "ms-dyn-sid") && !strings.EqualFold(name, "Cookie")
			})
			request["cookies"] = []any{}
		})
	})

	_, err := capture.Parse(strings.NewReader(har))
	var credentialErr *capture.CredentialError
	if !errors.As(err, &credentialErr) {
		t.Fatalf("Parse() error = %v, want *CredentialError", err)
	}
	if got, want := credentialErr.MissingHeaders, []string{"ms-dyn-sid"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MissingHeaders = %v, want %v", got, want)
	}
	if got, want := credentialErr.MissingCookies, []string{"DynamicsOwinAuth", "ms-dyn-csrftoken"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MissingCookies = %v, want %v", got, want)
	}
}

func TestParseRejectsRedactedCookies(t *testing.T) {
	_, err := capture.Parse(strings.NewReader(validHARWith(t, fixtureOptions{cookieValue: "[REDACTED]"})))
	var credentialErr *capture.CredentialError
	if !errors.As(err, &credentialErr) {
		t.Fatalf("Parse() error = %v, want *CredentialError", err)
	}
	if got, want := credentialErr.RedactedCookies, []string{"DynamicsOwinAuth", "backend-affinity", "ms-dyn-csrftoken"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RedactedCookies = %v, want %v", got, want)
	}
}

func TestParseRequiresAcknowledgedSuccessfulCreateRequest(t *testing.T) {
	har := mutateHAR(t, validHAR(t), func(document map[string]any) {
		entries := harEntries(document)
		createResponse := entries[2].(map[string]any)["response"].(map[string]any)
		content := createResponse["content"].(map[string]any)
		var body map[string]any
		if err := json.Unmarshal([]byte(content["text"].(string)), &body); err != nil {
			t.Fatalf("decode response fixture: %v", err)
		}
		interactions := body["Messages"].([]any)[0].(map[string]any)["Interactions"].([]any)
		interactions[0].(map[string]any)["CallbackId"] = "callback-fail"
		content["text"] = mustJSON(body)
	})

	_, err := capture.Parse(strings.NewReader(har))
	if err == nil || !strings.Contains(err.Error(), "successful SetValue+InvokeDefaultButton") {
		t.Fatalf("Parse() error = %v, want missing successful create request", err)
	}
}

func TestParseAcceptsBase64ResponseBodies(t *testing.T) {
	har := mutateHAR(t, validHAR(t), func(document map[string]any) {
		for _, rawEntry := range harEntries(document) {
			entry := rawEntry.(map[string]any)
			request := entry["request"].(map[string]any)
			if !strings.Contains(request["url"].(string), "ReliableCommunicationManager.svc/ProcessMessages") {
				continue
			}
			content := entry["response"].(map[string]any)["content"].(map[string]any)
			content["text"] = base64.StdEncoding.EncodeToString([]byte(content["text"].(string)))
			content["encoding"] = "base64"
		}
	})

	if _, err := capture.Parse(strings.NewReader(har)); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestLoadReadsHARFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.har")
	if err := os.WriteFile(path, []byte(validHAR(t)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	profile, err := capture.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := profile.Draft.SaveAndClose.ControlName, "SaveAndClose"; got != want {
		t.Errorf("SaveAndClose.ControlName = %q, want %q", got, want)
	}
}

func TestParseRejectsUnsupportedOrIrrelevantHAR(t *testing.T) {
	tests := []struct {
		name string
		har  string
		want string
	}{
		{name: "unsupported version", har: `{"log":{"version":"1.1","entries":[]}}`, want: "unsupported HAR version"},
		{name: "no process messages", har: `{"log":{"version":"1.2","entries":[]}}`, want: "no Dynamics ProcessMessages"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := capture.Parse(strings.NewReader(test.har))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestParseRejectsMixedProcessMessageSessions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, map[string]any)
	}{
		{
			name: "origin",
			mutate: func(t *testing.T, document map[string]any) {
				request := harEntries(document)[4].(map[string]any)["request"].(map[string]any)
				request["url"] = strings.Replace(request["url"].(string), "tenant.operations.dynamics.com", "other.operations.dynamics.com", 1)
			},
		},
		{
			name: "endpoint",
			mutate: func(t *testing.T, document map[string]any) {
				request := harEntries(document)[4].(map[string]any)["request"].(map[string]any)
				request["url"] = strings.Replace(request["url"].(string), "/Services/", "/alternate/Services/", 1)
			},
		},
		{
			name: "channel",
			mutate: func(t *testing.T, document map[string]any) {
				mutateEntryRequestEnvelope(t, document, 4, func(body map[string]any) { body["ChannelId"] = 8 })
			},
		},
		{
			name: "company",
			mutate: func(t *testing.T, document map[string]any) {
				mutateEntryRequestEnvelope(t, document, 4, func(body map[string]any) { body["CompanyId"] = "DEMF" })
			},
		},
		{
			name: "language",
			mutate: func(t *testing.T, document map[string]any) {
				mutateEntryRequestEnvelope(t, document, 4, func(body map[string]any) { body["Language"] = "fr-fr" })
			},
		},
		{
			name: "browser session header",
			mutate: func(t *testing.T, document map[string]any) {
				request := harEntries(document)[4].(map[string]any)["request"].(map[string]any)
				for _, raw := range request["headers"].([]any) {
					header := raw.(map[string]any)
					if strings.EqualFold(header["name"].(string), "ms-dyn-bsid") {
						header["value"] = "different-browser-session"
					}
				}
			},
		},
		{
			name: "authentication cookie",
			mutate: func(t *testing.T, document map[string]any) {
				request := harEntries(document)[4].(map[string]any)["request"].(map[string]any)
				for _, raw := range request["cookies"].([]any) {
					cookie := raw.(map[string]any)
					if strings.EqualFold(cookie["name"].(string), "DynamicsOwinAuth") {
						cookie["value"] = "different-auth-session"
					}
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			har := mutateHAR(t, validHAR(t), func(document map[string]any) {
				test.mutate(t, document)
			})
			_, err := capture.Parse(strings.NewReader(har))
			if err == nil || !strings.Contains(err.Error(), "mixed ProcessMessages sessions") {
				t.Fatalf("Parse() error = %v, want mixed ProcessMessages sessions", err)
			}
		})
	}
}

func TestParseRejectsTrailingSessionStateFromAnotherGroup(t *testing.T) {
	har := mutateHAR(t, validHAR(t), func(document map[string]any) {
		entries := harEntries(document)
		entries = append(entries, cloneJSONValue(t, entries[4]))
		document["log"].(map[string]any)["entries"] = entries
		request := entries[len(entries)-1].(map[string]any)["request"].(map[string]any)
		request["url"] = "https://other.operations.dynamics.com/Services/ReliableCommunicationManager.svc/ProcessMessages?cmp=DEMF&lng=fr-fr"
		mutateEntryRequestEnvelope(t, document, len(entries)-1, func(body map[string]any) {
			body["ChannelId"] = 99
			body["CompanyId"] = "DEMF"
			body["Language"] = "fr-fr"
		})
	})

	_, err := capture.Parse(strings.NewReader(har))
	if err == nil || !strings.Contains(err.Error(), "mixed ProcessMessages sessions") {
		t.Fatalf("Parse() error = %v, want mixed ProcessMessages sessions", err)
	}
}

func TestParseRejectsUnrelatedDraftCommandFlows(t *testing.T) {
	tests := []struct {
		name string
		har  func(*testing.T) string
		want string
	}{
		{
			name: "NamePurpose belongs to another dialog",
			har: func(t *testing.T) string {
				return mutateHAR(t, validHAR(t), func(document map[string]any) {
					mutateEntryResponseEnvelope(t, document, 0, func(body map[string]any) {
						descriptor := body["Messages"].([]any)[0].(map[string]any)["Interactions"].([]any)[0].(map[string]any)["Descriptor"].(map[string]any)
						descriptor["Name"] = "UnrelatedDialog_form"
					})
				})
			},
			want: "SetValue+InvokeDefaultButton",
		},
		{
			name: "InvokeDefaultButton uses another root",
			har: func(t *testing.T) string {
				return mutateHAR(t, validHAR(t), func(document map[string]any) {
					mutateEntryRequestEnvelope(t, document, 2, func(body map[string]any) {
						invoke := body["Messages"].([]any)[1].(map[string]any)["Interactions"].([]any)[0].(map[string]any)
						invoke["RootId"] = "workspace-root"
						invoke["TargetId"] = "workspace-root"
					})
				})
			},
			want: "SetValue+InvokeDefaultButton",
		},
		{
			name: "SaveAndClose belongs to another form",
			har: func(t *testing.T) string {
				return mutateHAR(t, validHAR(t), func(document map[string]any) {
					mutateEntryResponseEnvelope(t, document, 2, func(body map[string]any) {
						descriptor := body["Messages"].([]any)[0].(map[string]any)["Interactions"].([]any)[1].(map[string]any)["Descriptor"].(map[string]any)
						descriptor["Name"] = "UnrelatedDetails_form"
					})
				})
			},
			want: "SaveAndClose request in ExpenseReportDetails_form",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := capture.Parse(strings.NewReader(test.har(t)))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestParseRejectsAmbiguousMultipleDraftFlows(t *testing.T) {
	har := mutateHAR(t, validHAR(t), func(document map[string]any) {
		entries := harEntries(document)
		for _, index := range []int{0, 2, 4} {
			entries = append(entries, cloneJSONValue(t, entries[index]))
		}
		document["log"].(map[string]any)["entries"] = entries
	})

	_, err := capture.Parse(strings.NewReader(har))
	if err == nil || !strings.Contains(err.Error(), "multiple complete") {
		t.Fatalf("Parse() error = %v, want ambiguous multiple complete flows", err)
	}
}

func TestParseRejectsAdditionalRedactionMasks(t *testing.T) {
	for _, value := range []string{
		"********",
		"Bearer ********",
		"<secret>",
		"[hidden]",
		"<masked>",
		"value-removed-by-exporter",
		"[omitted]",
		"OMISSION",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := capture.Parse(strings.NewReader(validHARWith(t, fixtureOptions{
				headerValue: value,
				cookieValue: value,
			})))
			if !errors.Is(err, capture.ErrCredentials) {
				t.Fatalf("Parse() error = %v, want errors.Is(ErrCredentials)", err)
			}
			var credentialErr *capture.CredentialError
			if !errors.As(err, &credentialErr) {
				t.Fatalf("Parse() error type = %T, want *CredentialError", err)
			}
			if len(credentialErr.RedactedHeaders) != 4 || len(credentialErr.RedactedCookies) != 3 {
				t.Fatalf("redacted headers/cookies = %v/%v, want all captured credentials rejected", credentialErr.RedactedHeaders, credentialErr.RedactedCookies)
			}
		})
	}
}

func TestParseRejectsNewReportSelectionPairedWithUnrelatedClickTarget(t *testing.T) {
	har := mutateHAR(t, validHAR(t), func(document map[string]any) {
		mutateEntryRequestEnvelope(t, document, 0, func(body map[string]any) {
			interactions := body["Messages"].([]any)[0].(map[string]any)["Interactions"].([]any)
			interactions[1].(map[string]any)["TargetId"] = "save-target"
		})
	})
	_, err := capture.Parse(strings.NewReader(har))
	if err == nil || !strings.Contains(err.Error(), "workspace new-report request") {
		t.Fatalf("Parse() error = %v, want missing workspace new-report request", err)
	}
}

func TestProfileValidateRequiresDraftSequenceHeadroom(t *testing.T) {
	profile, err := capture.Parse(strings.NewReader(validHAR(t)))
	if err != nil {
		t.Fatal(err)
	}
	profile.Session.NextClientSequence = math.MaxInt64 - 2
	if err := profile.Validate(); err == nil || !strings.Contains(err.Error(), "headroom") {
		t.Fatalf("Validate() error = %v, want sequence headroom error", err)
	}
}
