// Package expense creates Dynamics 365 expense reports and either saves them as
// Drafts or explicitly submits them.
package expense

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

const (
	processMessagesPath = "/Services/ReliableCommunicationManager.svc/ProcessMessages"
	maxResponseBody     = 4 << 20
)

var errRedirect = errors.New("expense: redirects are not allowed")

// ErrAuthenticationExpired indicates that Dynamics redirected to sign-in or rejected the captured credentials.
var ErrAuthenticationExpired = errRedirect

// Option is a sealed Client configuration option.
type Option interface {
	apply(*clientOptions) error
}

type clientOptions struct {
	httpClient *http.Client
}

type httpClientOption struct {
	client *http.Client
}

func (option httpClientOption) apply(options *clientOptions) error {
	if option.client == nil {
		return errors.New("expense: HTTP client is nil")
	}
	options.httpClient = option.client
	return nil
}

// WithHTTPClient configures the HTTP client used for Dynamics requests.
func WithHTTPClient(client *http.Client) Option {
	return httpClientOption{client: client}
}

// Client is a narrowly allowlisted, stateful Dynamics expense client.
type Client struct {
	mu sync.Mutex

	httpClient *http.Client
	baseURL    string
	endpoint   *url.URL
	origin     string
	headers    http.Header
	cookies    []*http.Cookie

	company   string
	language  string
	channelID int

	lastServerSequence int64
	nextClientSequence int64

	workspaceRootID string
	createButtonID  string
}

// New validates and snapshots a captured Dynamics session.
func New(profile *capture.Profile, options ...Option) (*Client, error) {
	if profile == nil {
		return nil, errors.New("expense: capture profile is nil")
	}
	if err := profile.Validate(); err != nil {
		return nil, fmt.Errorf("expense: validate capture profile: %w", err)
	}
	if err := validateDraftFlow(profile.Draft); err != nil {
		return nil, err
	}
	return newClient(profile.Session, profile.Draft.NewReport, options...)
}

// NewFromBootstrap validates and snapshots a current Expense workspace session
// without requiring a previously captured draft create/save flow.
func NewFromBootstrap(profile *capture.BootstrapProfile, options ...Option) (*Client, error) {
	if profile == nil {
		return nil, errors.New("expense: bootstrap profile is nil")
	}
	if err := profile.Validate(); err != nil {
		return nil, fmt.Errorf("expense: validate bootstrap profile: %w", err)
	}
	return newClient(profile.Session, profile.NewReport, options...)
}

func newClient(session capture.SessionProfile, newReport capture.CommandTarget, options ...Option) (*Client, error) {
	endpoint, origin, err := validateEndpoint(session)
	if err != nil {
		return nil, err
	}
	headers, err := safeHeaders(session.RequestHeaders, origin)
	if err != nil {
		return nil, err
	}

	configuration := clientOptions{httpClient: http.DefaultClient}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("expense: nil option")
		}
		if err := option.apply(&configuration); err != nil {
			return nil, err
		}
	}

	httpClient := *configuration.httpClient
	httpClient.Jar = nil
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errRedirect
	}

	return &Client{
		httpClient:         &httpClient,
		baseURL:            session.BaseURL,
		endpoint:           endpoint,
		origin:             origin,
		headers:            headers,
		cookies:            cloneCookies(session.Cookies),
		company:            session.Company,
		language:           session.Language,
		channelID:          int(session.ChannelID),
		lastServerSequence: session.LastServerSequence,
		nextClientSequence: session.NextClientSequence,
		workspaceRootID:    newReport.RootID,
		createButtonID:     newReport.TargetID,
	}, nil
}

// SnapshotBootstrapProfile returns a validated, deep-copied snapshot of the
// current replayable workspace state. Credential values are retained for
// persistence and replay, so callers must protect the returned profile.
func (client *Client) SnapshotBootstrapProfile() (*capture.BootstrapProfile, error) {
	if client == nil {
		return nil, errors.New("expense: client is nil")
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	profile := &capture.BootstrapProfile{
		Session: capture.SessionProfile{
			BaseURL:            client.baseURL,
			EndpointURL:        client.endpoint.String(),
			RequestHeaders:     client.headers.Clone(),
			Cookies:            cloneCookies(client.cookies),
			Company:            client.company,
			Language:           client.language,
			ChannelID:          int64(client.channelID),
			LastServerSequence: client.lastServerSequence,
			NextClientSequence: client.nextClientSequence,
		},
		NewReport: capture.CommandTarget{
			CommandName: dynamics.CommandClick,
			RootID:      client.workspaceRootID,
			TargetID:    client.createButtonID,
			ControlName: dynamics.SelectedControlNewExpenseReportReportsTab,
		},
	}
	if err := profile.Validate(); err != nil {
		return nil, fmt.Errorf("expense: validate bootstrap snapshot: %w", err)
	}
	return profile, nil
}

// BootstrapProfile is a compatibility-friendly alias for
// SnapshotBootstrapProfile.
func (client *Client) BootstrapProfile() (*capture.BootstrapProfile, error) {
	return client.SnapshotBootstrapProfile()
}

// PlanCreateReport returns an offline, credential-free operation plan.
func (client *Client) PlanCreateReport(request CreateReportRequest) (CreateReportPlan, error) {
	if err := validatePurpose(request.Purpose); err != nil {
		return CreateReportPlan{}, err
	}
	if err := validateReportFinalAction(request.FinalAction); err != nil {
		return CreateReportPlan{}, err
	}
	finalAction := "save and close draft"
	if request.FinalAction == ReportFinalActionSubmit {
		finalAction = "submit the new report using its exact discovered SubmitButton"
	}
	return CreateReportPlan{
		Purpose:      request.Purpose,
		RequestCount: 3,
		Actions: []string{
			"open new expense report",
			"set purpose and create draft",
			finalAction,
		},
	}, nil
}

// CreateReport creates a report and performs its explicit final action.
func (client *Client) CreateReport(ctx context.Context, request CreateReportRequest) (ReportResult, error) {
	if ctx == nil {
		return ReportResult{}, errors.New("expense: context is nil")
	}
	if err := validatePurpose(request.Purpose); err != nil {
		return ReportResult{}, err
	}
	if err := validateReportFinalAction(request.FinalAction); err != nil {
		return ReportResult{}, err
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if client.nextClientSequence > math.MaxInt64-3 {
		return ReportResult{}, errors.New("expense: client sequence lacks headroom for report creation")
	}

	draft, err := client.createDraftDetails(ctx, request.Purpose)
	if err != nil {
		return ReportResult{}, err
	}

	if request.FinalAction == ReportFinalActionSubmit {
		status, err := client.submitOpenDraft(ctx, draft.reportNumber, draft.detailsRootID, draft.submitButton)
		if err != nil {
			return ReportResult{}, err
		}
		return ReportResult{
			Purpose:      request.Purpose,
			ReportNumber: draft.reportNumber,
			Status:       status,
			Submitted:    true,
		}, nil
	}

	save := dynamics.BuildSaveAndCloseClickMessage(client.nextClientSequence, draft.detailsRootID, draft.saveAndCloseID)
	if _, err := client.send(ctx, []dynamics.Message{save}, dynamics.DraftCommandTargets{
		DetailsRootID:  draft.detailsRootID,
		SaveAndCloseID: draft.saveAndCloseID,
	}); err != nil {
		return ReportResult{}, fmt.Errorf("expense: save and close draft: %w", err)
	}

	return ReportResult{
		Purpose:        request.Purpose,
		ReportNumber:   draft.reportNumber,
		Status:         draft.status,
		SavedAndClosed: true,
	}, nil
}

type openDraftDetails struct {
	responseBody   []byte
	reportNumber   string
	status         string
	detailsRootID  string
	saveAndCloseID string
	submitButton   dynamics.ModelNode
}

// createDraftDetails creates a Draft and leaves its details form open. The
// caller must hold client.mu and reserve enough sequence headroom for its
// remaining workflow.
func (client *Client) createDraftDetails(ctx context.Context, purpose string) (openDraftDetails, error) {
	openSequence := client.nextClientSequence
	open := dynamics.BuildOpenNewExpenseReportMessage(openSequence, client.workspaceRootID, client.createButtonID)
	openBody, err := client.send(ctx, []dynamics.Message{open}, dynamics.DraftCommandTargets{
		WorkspaceRootID: client.workspaceRootID,
		CreateButtonID:  client.createButtonID,
	})
	if err != nil {
		return openDraftDetails{}, fmt.Errorf("expense: open new report: %w", err)
	}

	openModel, err := dynamics.DiscoverResponseModel(openBody)
	if err != nil {
		return openDraftDetails{}, err
	}
	dialog, ok := openModel.FindForm(dynamics.FormExpenseNewExpenseReport)
	if !ok || dialog.ID == "" {
		return openDraftDetails{}, errors.New("expense: new-expense-report form not found")
	}
	purposeControl, ok := openModel.FindControl(dynamics.ControlNamePurpose)
	if !ok || purposeControl.ID == "" || purposeControl.RootID != dialog.ID {
		return openDraftDetails{}, errors.New("expense: NamePurpose control not found in new-expense-report form")
	}

	setSequence := client.nextClientSequence
	set := dynamics.BuildSetValueMessage(setSequence, dialog.ID, purposeControl.ID, purpose, "", "")
	invoke := dynamics.BuildInvokeDefaultButtonMessage(setSequence+1, dialog.ID)
	createBody, err := client.send(ctx, []dynamics.Message{set, invoke}, dynamics.DraftCommandTargets{
		DialogRootID:  dialog.ID,
		NamePurposeID: purposeControl.ID,
	})
	if err != nil {
		return openDraftDetails{}, fmt.Errorf("expense: create draft: %w", err)
	}

	createModel, err := dynamics.DiscoverResponseModel(createBody)
	if err != nil {
		return openDraftDetails{}, err
	}
	details, ok := createModel.FindForm(dynamics.FormExpenseReportDetails)
	if !ok || details.ID == "" {
		return openDraftDetails{}, errors.New("expense: expense-report-details form not found")
	}
	saveAndClose, ok := createModel.FindControl(dynamics.ControlSaveAndClose)
	if !ok || saveAndClose.ID == "" || saveAndClose.RootID != details.ID {
		return openDraftDetails{}, errors.New("expense: SaveAndClose control not found in expense-report-details form")
	}
	if strings.TrimSpace(createModel.ReportNumber) == "" {
		return openDraftDetails{}, errors.New("expense: draft report number is missing")
	}
	if !strings.EqualFold(strings.TrimSpace(createModel.Status), "Draft") {
		return openDraftDetails{}, fmt.Errorf("expense: report status is not Draft: %q", createModel.Status)
	}
	submitButton, _ := createModel.FindControlInRoot(dynamics.ControlSubmitButton, details.ID)

	return openDraftDetails{
		responseBody:   createBody,
		reportNumber:   createModel.ReportNumber,
		status:         createModel.Status,
		detailsRootID:  details.ID,
		saveAndCloseID: saveAndClose.ID,
		submitButton:   submitButton,
	}, nil
}

func (client *Client) submitOpenDraft(ctx context.Context, reportNumber, detailsRootID string, submitButton dynamics.ModelNode) (string, error) {
	if err := dynamics.ValidateSubmitButton(submitButton, detailsRootID); err != nil {
		return "", fmt.Errorf("expense: submit control is unavailable or unsupported: %w", err)
	}
	submit := dynamics.BuildSubmitClickMessage(client.nextClientSequence, detailsRootID, submitButton.ID)
	body, err := client.sendValidated(ctx, []dynamics.Message{submit}, func(envelope dynamics.Envelope) error {
		return dynamics.ValidateSubmitCommands(envelope, dynamics.SubmitCommandTargets{
			DetailsRootID:  detailsRootID,
			SubmitButtonID: submitButton.ID,
		})
	})
	if err != nil {
		return "", fmt.Errorf("expense: submit report: %w", err)
	}

	status, found, err := submittedReportStatus(body, reportNumber)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("expense: submit response did not verify the created report's new status")
	}
	if isDraftStatus(status) {
		return "", errors.New("expense: submit response still reports the created report as Draft")
	}
	if !isSubmittedStatus(status) {
		return "", fmt.Errorf("expense: submit response did not affirmatively report the created report as Submitted: %q", status)
	}
	model, err := dynamics.DiscoverResponseModel(body)
	if err != nil {
		return "", err
	}
	workspace, ok := model.FindForm(dynamics.FormExpenseWorkspace)
	if !ok || workspace.ID == "" {
		return "", errors.New("expense: submit response did not restore the Expense workspace")
	}
	newReport, ok := model.FindControl(dynamics.SelectedControlNewExpenseReportReportsTab)
	if !ok || newReport.ID == "" || newReport.RootID != workspace.ID {
		return "", errors.New("expense: submit response did not restore the New expense report control")
	}
	client.workspaceRootID = workspace.ID
	client.createButtonID = newReport.ID
	return status, nil
}

func (client *Client) send(ctx context.Context, messages []dynamics.Message, targets dynamics.DraftCommandTargets) ([]byte, error) {
	return client.sendValidated(ctx, messages, func(envelope dynamics.Envelope) error {
		return dynamics.ValidateDraftCommands(envelope, targets)
	})
}

func (client *Client) sendValidated(ctx context.Context, messages []dynamics.Message, validate func(dynamics.Envelope) error) ([]byte, error) {
	if len(messages) == 0 {
		return nil, errors.New("expense: Dynamics request has no messages")
	}
	if validate == nil {
		return nil, errors.New("expense: Dynamics request validator is nil")
	}
	envelope := dynamics.Envelope{
		ChannelID:                      client.channelID,
		CompanyID:                      client.company,
		Language:                       client.language,
		LastAcknowledgedSequenceNumber: client.lastServerSequence,
		Messages:                       messages,
	}
	if err := validate(envelope); err != nil {
		return nil, err
	}
	body, err := dynamics.MarshalEnvelope(envelope)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	request.Header = client.headers.Clone()
	request.Header.Set("Origin", client.origin)
	request.Header.Set("Content-Type", "application/json; charset=UTF-8")
	for _, cookie := range client.cookies {
		request.AddCookie(cookie)
	}

	response, err := client.httpClient.Do(request)
	if response != nil {
		mergeSameOriginResponseState(client.origin, client.headers, &client.cookies, response)
	}
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if errors.Is(err, errRedirect) {
			return nil, errRedirect
		}
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, maxResponseBody+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(responseBody) > maxResponseBody {
		return nil, errors.New("expense: Dynamics response body exceeds limit")
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, ErrAuthenticationExpired
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("expense: Dynamics endpoint returned HTTP %d", response.StatusCode)
	}

	responseEnvelope, err := dynamics.ParseEnvelope(responseBody)
	if err != nil {
		return nil, err
	}
	if responseEnvelope.ChannelID != client.channelID {
		return nil, errors.New("expense: Dynamics response channel does not match captured session")
	}
	lastSent := messages[len(messages)-1].SequenceNumber
	if responseEnvelope.LastAcknowledgedSequenceNumber != lastSent {
		return nil, fmt.Errorf("expense: stale Dynamics response acknowledgement: got %d, want %d", responseEnvelope.LastAcknowledgedSequenceNumber, lastSent)
	}

	maximumServerSequence := client.lastServerSequence
	for _, message := range responseEnvelope.Messages {
		if message.SequenceNumber <= client.lastServerSequence {
			return nil, fmt.Errorf("expense: stale Dynamics server sequence %d", message.SequenceNumber)
		}
		if message.SequenceNumber > maximumServerSequence {
			maximumServerSequence = message.SequenceNumber
		}
	}
	if responseEnvelope.LastAcknowledgedSequenceNumber == math.MaxInt64 {
		return nil, errors.New("expense: Dynamics acknowledgement exhausted client sequence space")
	}
	client.lastServerSequence = maximumServerSequence
	client.nextClientSequence = responseEnvelope.LastAcknowledgedSequenceNumber + 1

	if issue := dynamicsIssue(responseBody); issue != "" {
		return nil, errors.New(issue)
	}
	return responseBody, nil
}

func validatePurpose(purpose string) error {
	if !utf8.ValidString(purpose) {
		return errors.New("expense: purpose is not valid UTF-8")
	}
	if strings.TrimSpace(purpose) == "" {
		return errors.New("expense: purpose is empty")
	}
	return nil
}

func validateDraftFlow(flow capture.DraftFlow) error {
	checks := []struct {
		gotCommand, wantCommand string
		gotControl, wantControl string
	}{
		{flow.NewReport.CommandName, dynamics.CommandClick, flow.NewReport.ControlName, dynamics.SelectedControlNewExpenseReportReportsTab},
		{flow.CreateDraft.SetValue.CommandName, dynamics.CommandSetValue, flow.CreateDraft.SetValue.ControlName, dynamics.ControlNamePurpose},
		{flow.CreateDraft.InvokeDefaultButton.CommandName, dynamics.CommandExecuteShortcuts, flow.CreateDraft.InvokeDefaultButton.ControlName, dynamics.FormExpenseNewExpenseReport},
		{flow.SaveAndClose.CommandName, dynamics.CommandClick, flow.SaveAndClose.ControlName, dynamics.ControlSaveAndClose},
	}
	for _, check := range checks {
		if check.gotCommand != check.wantCommand || check.gotControl != check.wantControl {
			return errors.New("expense: capture does not describe the allowlisted draft flow")
		}
	}
	return nil
}

func validateEndpoint(session capture.SessionProfile) (*url.URL, string, error) {
	base, err := url.Parse(session.BaseURL)
	if err != nil {
		return nil, "", errors.New("expense: invalid capture base URL")
	}
	endpoint, err := url.Parse(session.EndpointURL)
	if err != nil {
		return nil, "", errors.New("expense: invalid capture endpoint URL")
	}
	if base.RawQuery != "" || base.Fragment != "" || endpoint.Fragment != "" || base.RawPath != "" || endpoint.RawPath != "" {
		return nil, "", errors.New("expense: capture URLs contain unsupported components")
	}
	origin := strings.ToLower(base.Scheme) + "://" + base.Host
	endpointOrigin := strings.ToLower(endpoint.Scheme) + "://" + endpoint.Host
	if origin != endpointOrigin {
		return nil, "", errors.New("expense: capture endpoint origin mismatch")
	}
	expectedPath := strings.TrimRight(base.Path, "/") + processMessagesPath
	if endpoint.Path != expectedPath {
		return nil, "", errors.New("expense: capture endpoint is not the exact ProcessMessages endpoint")
	}
	query := endpoint.Query()
	if len(query) != 2 || len(query["cmp"]) != 1 || query.Get("cmp") != session.Company || len(query["lng"]) != 1 || query.Get("lng") != session.Language {
		return nil, "", errors.New("expense: capture endpoint company or language mismatch")
	}
	return endpoint, origin, nil
}

func safeHeaders(source http.Header, origin string) (http.Header, error) {
	destination := make(http.Header)
	for name, values := range source {
		lower := strings.ToLower(strings.TrimSpace(name))
		if !safeHeaderName(lower) {
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
	if capturedOrigin := destination.Values("Origin"); len(capturedOrigin) != 0 {
		for _, value := range capturedOrigin {
			if !strings.EqualFold(strings.TrimSpace(value), origin) {
				return nil, errors.New("expense: captured Origin header does not match endpoint origin")
			}
		}
	}
	if referer := destination.Get("Referer"); referer != "" {
		refererURL, err := url.Parse(referer)
		if err != nil || strings.ToLower(refererURL.Scheme)+"://"+refererURL.Host != origin {
			return nil, errors.New("expense: captured Referer header does not match endpoint origin")
		}
	}
	return destination, nil
}

func safeHeaderName(name string) bool {
	switch name {
	case "accept", "accept-language", "authorization", "content-type", "origin", "referer", "user-agent", "x-requested-with":
		return true
	default:
		return strings.HasPrefix(name, "ms-dyn-")
	}
}

func dynamicsIssue(data []byte) string {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return ""
	}
	severity := issueSeverity(value)
	switch severity {
	case 2:
		return "expense: Dynamics response contains an error message"
	case 1:
		return "expense: Dynamics response contains a warning message"
	default:
		return ""
	}
}

func issueSeverity(value any) int {
	maximum := 0
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			if properties, ok := typed["Properties"].(map[string]any); ok {
				if _, hasText := properties["text"]; hasText {
					maximum = max(maximum, parseSeverity(properties["kind"]))
				}
			}
			if interactionType, ok := typed["$type"].(string); ok {
				normalized := strings.ToLower(interactionType)
				if strings.Contains(normalized, "errorinteraction") || strings.Contains(normalized, "exceptioninteraction") {
					maximum = max(maximum, 2)
				} else if strings.Contains(normalized, "warninginteraction") {
					maximum = max(maximum, 1)
				}
			}
			for _, key := range []string{"Severity", "severity", "Level", "level", "MessageType", "messageType"} {
				if severity, ok := typed[key]; ok {
					maximum = max(maximum, parseSeverity(severity))
				}
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return maximum
}

func parseSeverity(value any) int {
	switch typed := value.(type) {
	case json.Number:
		number, _ := strconv.Atoi(typed.String())
		if number >= 2 {
			return 2
		}
		if number == 1 {
			return 1
		}
	case float64:
		if typed >= 2 {
			return 2
		}
		if typed == 1 {
			return 1
		}
	case string:
		normalized := strings.ToLower(strings.TrimSpace(typed))
		if number, err := strconv.Atoi(normalized); err == nil {
			return parseSeverity(json.Number(strconv.Itoa(number)))
		}
		if strings.Contains(normalized, "error") || strings.Contains(normalized, "fatal") || strings.Contains(normalized, "exception") {
			return 2
		}
		if strings.Contains(normalized, "warn") {
			return 1
		}
	}
	return 0
}
