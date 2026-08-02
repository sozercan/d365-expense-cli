package capture_test

import (
	"encoding/json"
	"strings"
	"testing"
)

type fixtureOptions struct {
	headerValue string
	cookieValue string
}

func validHAR(t *testing.T) string {
	t.Helper()
	return validHARWith(t, fixtureOptions{})
}

func validHARWith(t *testing.T, options fixtureOptions) string {
	t.Helper()

	headerValue := options.headerValue
	if headerValue == "" {
		headerValue = "captured-header-secret"
	}
	cookieValue := options.cookieValue
	if cookieValue == "" {
		cookieValue = "captured-cookie-secret"
	}

	endpoint := fixtureEndpoint
	headers := fixtureHeaders(headerValue, cookieValue)
	cookies := fixtureCookies(cookieValue)

	newReportRequest := envelope(7, "USMF", "en-us", 40,
		message(100,
			command("UpdateLastSelectedControl", "workspace-root", "workspace-root", "", "", []any{"NewExpenseReportReportsTab"}),
			command("Click", "workspace-root", "new-report-target", "", "", []any{}),
		),
	)
	newReportResponse := envelope(7, "", "", 100,
		message(41,
			map[string]any{
				"$type":  "CreateViewModelInteraction",
				"RootId": "new-report-root",
				"Descriptor": map[string]any{
					"Id":   "new-report-root",
					"Name": "ExpenseNewExpenseReport_form",
					"ChildViewModels": []any{
						map[string]any{"Id": "purpose-target", "Name": "NamePurpose"},
					},
				},
			},
		),
	)

	createRequest := envelope(7, "USMF", "en-us", 41,
		message(101,
			command("SetValue", "new-report-root", "purpose-target", "callback-ok", "callback-fail", []any{"Team dinner"}),
		),
		message(102,
			command("ExecuteShortcuts", "new-report-root", "new-report-root", "", "", []any{"InvokeDefaultButton"}),
		),
	)
	createResponse := envelope(7, "", "", 102,
		message(42,
			map[string]any{
				"$type":            "CommandCallbackInteraction",
				"CallbackId":       "callback-ok",
				"UnusedCallbackId": "callback-fail",
				"Result":           map[string]any{"Value": "1"},
			},
			map[string]any{
				"$type":  "CreateViewModelInteraction",
				"RootId": "details-root",
				"Descriptor": map[string]any{
					"Id":   "details-root",
					"Name": "ExpenseReportDetails_form",
					"ChildViewModels": []any{
						map[string]any{"Id": "save-target", "Name": "SaveAndClose"},
					},
				},
			},
		),
	)

	saveRequest := envelope(7, "USMF", "en-us", 42,
		message(103,
			command("Click", "details-root", "save-target", "", "", []any{}),
		),
	)
	saveResponse := envelope(7, "", "", 103,
		message(43,
			map[string]any{
				"$type":  "UpdateViewModelInteraction",
				"RootId": "workspace-root",
				"Descriptor": map[string]any{
					"Id":   "workspace-root",
					"Name": "ExpenseWorkspace_form",
					"ChildViewModels": []any{
						map[string]any{"Id": "new-report-target", "Name": "NewExpenseReportReportsTab"},
					},
				},
			},
		),
	)

	document := map[string]any{
		"log": map[string]any{
			"version": "1.2",
			"creator": map[string]any{"name": "unit-test", "version": "1"},
			"entries": []any{
				entry("POST", endpoint, headers, cookies, newReportRequest, 200, newReportResponse),
				entry("POST", "https://tenant.operations.dynamics.com/Services/TelemetryManager.svc/ProcessEventData", headers, cookies, map[string]any{"event": "ignored"}, 200, map[string]any{}),
				entry("POST", endpoint, headers, cookies, createRequest, 200, createResponse),
				entry("GET", "https://tenant.operations.dynamics.com/resources/icon.svg", nil, nil, nil, 200, nil),
				entry("POST", endpoint, headers, cookies, saveRequest, 200, saveResponse),
			},
		},
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("json.Marshal(fixture) error = %v", err)
	}
	return string(encoded)
}

const fixtureEndpoint = "https://tenant.operations.dynamics.com/Services/ReliableCommunicationManager.svc/ProcessMessages?cmp=USMF&lng=en-us"

func validBootstrapHAR(t *testing.T) string {
	t.Helper()
	document := map[string]any{
		"log": map[string]any{
			"version": "1.2",
			"creator": map[string]any{"name": "unit-test", "version": "1"},
			"entries": []any{
				bootstrapWorkspaceEntry(7, 40, 100, 41, "bootstrap-header-secret", "bootstrap-cookie-secret", "workspace-root", "new-report-target"),
			},
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("json.Marshal(bootstrap fixture) error = %v", err)
	}
	return string(encoded)
}

func bootstrapHARWithInitializationGroups(t *testing.T) string {
	t.Helper()
	document := map[string]any{
		"log": map[string]any{
			"version": "1.2",
			"creator": map[string]any{"name": "unit-test", "version": "1"},
			"entries": []any{
				bootstrapInitializationEntry(1, 5, 10, 6, "init-one-header", "init-one-cookie"),
				bootstrapInitializationEntry(2, 15, 20, 16, "init-two-header", "init-two-cookie"),
				bootstrapWorkspaceEntry(7, 40, 100, 41, "bootstrap-header-secret", "bootstrap-cookie-secret", "workspace-root", "new-report-target"),
			},
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("json.Marshal(initialization bootstrap fixture) error = %v", err)
	}
	return string(encoded)
}

func bootstrapWorkspaceEntry(channel, requestAck, clientSequence, serverSequence int64, headerValue, cookieValue, rootID, targetID string) map[string]any {
	request := envelope(channel, "USMF", "en-us", requestAck, message(clientSequence))
	response := envelope(channel, "", "", clientSequence,
		message(serverSequence, map[string]any{
			"$type":  "UpdateViewModelInteraction",
			"RootId": rootID,
			"Descriptor": map[string]any{
				"Id":   rootID,
				"Name": "ExpenseWorkspace_form",
				"ChildViewModels": []any{
					map[string]any{"Id": targetID, "Name": "NewExpenseReportReportsTab"},
				},
			},
		}),
	)
	return entry("POST", fixtureEndpoint, fixtureHeaders(headerValue, cookieValue), fixtureCookies(cookieValue), request, 200, response)
}

func bootstrapInitializationEntry(channel, requestAck, clientSequence, serverSequence int64, headerValue, cookieValue string) map[string]any {
	request := envelope(channel, "USMF", "en-us", requestAck, message(clientSequence))
	response := envelope(channel, "", "", clientSequence,
		message(serverSequence, map[string]any{
			"$type":  "UpdateViewModelInteraction",
			"RootId": "initialization-root",
			"Descriptor": map[string]any{
				"Id":   "initialization-root",
				"Name": "Initialization_form",
			},
		}),
	)
	return entry("POST", fixtureEndpoint, fixtureHeaders(headerValue, cookieValue), fixtureCookies(cookieValue), request, 200, response)
}

func fixtureHeaders(headerValue, cookieValue string) []any {
	return []any{
		nameValue("Accept", "application/json"),
		nameValue("Accept-Encoding", "gzip, deflate, br"),
		nameValue("Connection", "keep-alive"),
		nameValue("Content-Length", "123"),
		nameValue("Content-Type", "application/json; charset=UTF-8"),
		nameValue("Cookie", "ms-dyn-csrftoken="+cookieValue+"; DynamicsOwinAuth="+cookieValue+"; backend-affinity="+cookieValue),
		nameValue("Host", "tenant.operations.dynamics.com"),
		nameValue("Origin", "https://tenant.operations.dynamics.com"),
		nameValue("Referer", "https://tenant.operations.dynamics.com/?cmp=USMF"),
		nameValue("Sec-Fetch-Mode", "cors"),
		nameValue("User-Agent", "fixture-agent"),
		nameValue("X-Requested-With", "XMLHttpRequest"),
		nameValue("ms-dyn-aid", headerValue),
		nameValue("ms-dyn-bsid", headerValue),
		nameValue("ms-dyn-csrftoken", headerValue),
		nameValue("ms-dyn-sid", headerValue),
		nameValue("sec-ch-ua", `"Fixture";v="1"`),
	}
}

func fixtureCookies(cookieValue string) []any {
	return []any{
		map[string]any{"name": "ms-dyn-csrftoken", "value": cookieValue, "path": "/", "secure": true, "httpOnly": true, "sameSite": "None"},
		map[string]any{"name": "DynamicsOwinAuth", "value": cookieValue, "path": "/", "secure": true, "httpOnly": true},
		map[string]any{"name": "backend-affinity", "value": cookieValue, "path": "/", "secure": true},
	}
}

func entry(method, url string, headers, cookies []any, requestBody any, status int, responseBody any) map[string]any {
	requestText := ""
	if requestBody != nil {
		requestText = mustJSON(requestBody)
	}
	responseText := ""
	if responseBody != nil {
		responseText = mustJSON(responseBody)
	}
	return map[string]any{
		"request": map[string]any{
			"method":   method,
			"url":      url,
			"headers":  headers,
			"cookies":  cookies,
			"postData": map[string]any{"mimeType": "application/json", "text": requestText},
		},
		"response": map[string]any{
			"status": status,
			"content": map[string]any{
				"mimeType": "application/json",
				"text":     responseText,
			},
		},
	}
}

func envelope(channel int64, company, language string, lastAcknowledged int64, messages ...any) map[string]any {
	body := map[string]any{
		"ChannelId":                      channel,
		"LastAcknowledgedSequenceNumber": lastAcknowledged,
		"Messages":                       messages,
	}
	if company != "" {
		body["CompanyId"] = company
	}
	if language != "" {
		body["Language"] = language
	}
	return body
}

func message(sequence int64, interactions ...any) map[string]any {
	return map[string]any{
		"SequenceNumber": sequence,
		"Interactions":   interactions,
	}
}

func command(name, rootID, targetID, callbackID, failureCallbackID string, positional []any) map[string]any {
	return map[string]any{
		"$type":                "CommandInteraction",
		"CommandName":          name,
		"RootId":               rootID,
		"TargetId":             targetID,
		"CallbackId":           callbackID,
		"FailureCallbackId":    failureCallbackID,
		"NamedParameters":      map[string]any{},
		"PositionalParameters": positional,
		"NoAsyncIncrement":     false,
		"PriorityPosition":     false,
		"ResetThrottleTime":    false,
		"Throttle":             false,
		"ThrottleId":           "0",
		"ThrottleTimestamp":    0,
		"ThrottleValue":        0,
		"Telemetry":            true,
	}
}

func nameValue(name, value string) map[string]any {
	return map[string]any{"name": name, "value": value}
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func mutateHAR(t *testing.T, source string, mutate func(map[string]any)) string {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(source), &document); err != nil {
		t.Fatalf("json.Unmarshal(HAR fixture) error = %v", err)
	}
	mutate(document)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("json.Marshal(mutated HAR fixture) error = %v", err)
	}
	return string(encoded)
}

func harEntries(document map[string]any) []any {
	return document["log"].(map[string]any)["entries"].([]any)
}

func forEachProcessRequest(document map[string]any, visit func(map[string]any)) {
	for _, rawEntry := range harEntries(document) {
		request := rawEntry.(map[string]any)["request"].(map[string]any)
		if url, _ := request["url"].(string); strings.Contains(url, "ReliableCommunicationManager.svc/ProcessMessages") {
			visit(request)
		}
	}
}

func filterNamedValues(values []any, keep func(name string) bool) []any {
	filtered := make([]any, 0, len(values))
	for _, value := range values {
		item := value.(map[string]any)
		if keep(item["name"].(string)) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func mutateEntryRequestEnvelope(t *testing.T, document map[string]any, entryIndex int, mutate func(map[string]any)) {
	t.Helper()
	request := harEntries(document)[entryIndex].(map[string]any)["request"].(map[string]any)
	postData := request["postData"].(map[string]any)
	var body map[string]any
	if err := json.Unmarshal([]byte(postData["text"].(string)), &body); err != nil {
		t.Fatalf("decode request envelope fixture: %v", err)
	}
	mutate(body)
	postData["text"] = mustJSON(body)
}

func mutateEntryResponseEnvelope(t *testing.T, document map[string]any, entryIndex int, mutate func(map[string]any)) {
	t.Helper()
	response := harEntries(document)[entryIndex].(map[string]any)["response"].(map[string]any)
	content := response["content"].(map[string]any)
	var body map[string]any
	if err := json.Unmarshal([]byte(content["text"].(string)), &body); err != nil {
		t.Fatalf("decode response envelope fixture: %v", err)
	}
	mutate(body)
	content["text"] = mustJSON(body)
}

func cloneJSONValue(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(clone fixture) error = %v", err)
	}
	var cloned any
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatalf("json.Unmarshal(clone fixture) error = %v", err)
	}
	return cloned
}
