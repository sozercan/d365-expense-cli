// Package capture extracts replayable Dynamics 365 Finance sessions and
// captured expense-report controls from HAR 1.2 recordings.
package capture

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
)

// ErrCredentials is matched by errors.Is when a capture cannot be replayed
// because required credentials are missing or were redacted.
var ErrCredentials = errors.New("capture credentials are missing or redacted")

// CredentialError describes unusable captured credentials without retaining or
// reporting any credential values.
type CredentialError struct {
	MissingHeaders  []string
	RedactedHeaders []string
	MissingCookies  []string
	RedactedCookies []string
}

func (e *CredentialError) Error() string {
	if e == nil {
		return ErrCredentials.Error()
	}
	parts := make([]string, 0, 4)
	if len(e.MissingHeaders) != 0 {
		parts = append(parts, "missing headers: "+strings.Join(e.MissingHeaders, ", "))
	}
	if len(e.RedactedHeaders) != 0 {
		parts = append(parts, "redacted headers: "+strings.Join(e.RedactedHeaders, ", "))
	}
	if len(e.MissingCookies) != 0 {
		parts = append(parts, "missing cookies: "+strings.Join(e.MissingCookies, ", "))
	}
	if len(e.RedactedCookies) != 0 {
		parts = append(parts, "redacted cookies: "+strings.Join(e.RedactedCookies, ", "))
	}
	if len(parts) == 0 {
		return ErrCredentials.Error()
	}
	return "capture credentials: " + strings.Join(parts, "; ")
}

// Unwrap allows callers to use errors.Is(err, ErrCredentials).
func (e *CredentialError) Unwrap() error { return ErrCredentials }

// Profile is the validated result of parsing a HAR capture.
type Profile struct {
	Session SessionProfile
	Draft   DraftFlow
}

// BootstrapProfile is a validated current Expense workspace session. Unlike
// Profile, it does not require a previously recorded draft create/save flow.
type BootstrapProfile struct {
	Session   SessionProfile
	NewReport CommandTarget
}

// SessionProfile contains the HTTP and ReliableCommunicationManager state
// needed to continue the captured session.
type SessionProfile struct {
	BaseURL            string
	EndpointURL        string
	RequestHeaders     http.Header
	Cookies            []*http.Cookie
	Company            string
	Language           string
	ChannelID          int64
	LastServerSequence int64
	NextClientSequence int64
}

// DraftFlow identifies the three requests in the captured expense draft flow.
type DraftFlow struct {
	NewReport    CommandTarget
	CreateDraft  CreateDraftRequest
	SaveAndClose CommandTarget
}

// CreateDraftRequest identifies the SetValue and InvokeDefaultButton commands
// that were observed together in one successful request.
type CreateDraftRequest struct {
	SetValue            CommandTarget
	InvokeDefaultButton CommandTarget
}

// CommandTarget identifies a captured command and its server-assigned controls.
type CommandTarget struct {
	CommandName string
	RootID      string
	TargetID    string
	ControlName string
}

// Load opens and parses a HAR 1.2 file.
func Load(path string) (*Profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open HAR: %w", err)
	}
	defer f.Close()

	profile, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("parse HAR: %w", err)
	}
	return profile, nil
}

// LoadBootstrap opens and parses a HAR 1.2 file containing a current Expense
// workspace session. A completed draft flow is not required.
func LoadBootstrap(path string) (*BootstrapProfile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open HAR: %w", err)
	}
	defer f.Close()

	profile, err := ParseBootstrap(f)
	if err != nil {
		return nil, fmt.Errorf("parse HAR: %w", err)
	}
	return profile, nil
}

// Parse reads a HAR 1.2 document and returns a complete, validated profile.
func Parse(r io.Reader) (*Profile, error) {
	profile, err := parseHAR(r)
	if err != nil {
		return nil, err
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	return profile, nil
}

// ParseBootstrap reads a HAR 1.2 document and returns a current Expense
// workspace session without requiring a captured create/save draft flow.
func ParseBootstrap(r io.Reader) (*BootstrapProfile, error) {
	profile, err := parseBootstrapHAR(r)
	if err != nil {
		return nil, err
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	return profile, nil
}

// ParseBootstrapWithNewReport imports current Dynamics protocol state from a
// HAR while using an independently discovered, exact New expense report
// control target. This is used by trusted local CDP acquisition when the
// workspace model is already rendered but not repeated in captured responses.
func ParseBootstrapWithNewReport(r io.Reader, newReport CommandTarget) (*BootstrapProfile, error) {
	profile, err := parseBootstrapSessionHAR(r)
	if err != nil {
		return nil, err
	}
	profile.NewReport = newReport
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	return profile, nil
}

// Validate verifies that the profile is complete and safe to execute.
func (p *Profile) Validate() error {
	if p == nil {
		return errors.New("capture profile is nil")
	}
	if err := validateSessionProfile(p.Session); err != nil {
		return err
	}
	if err := validateTarget("new-report", p.Draft.NewReport); err != nil {
		return err
	}
	if err := validateTarget("SetValue", p.Draft.CreateDraft.SetValue); err != nil {
		return err
	}
	if err := validateTarget("InvokeDefaultButton", p.Draft.CreateDraft.InvokeDefaultButton); err != nil {
		return err
	}
	if err := validateTarget("SaveAndClose", p.Draft.SaveAndClose); err != nil {
		return err
	}
	return nil
}

// Validate verifies that the bootstrap profile identifies an exact allowlisted
// current Expense workspace target and a replayable session.
func (p *BootstrapProfile) Validate() error {
	if p == nil {
		return errors.New("capture bootstrap profile is nil")
	}
	if err := validateSessionProfile(p.Session); err != nil {
		return err
	}
	if err := validateTarget("new-report", p.NewReport); err != nil {
		return err
	}
	if p.NewReport.CommandName != "Click" || p.NewReport.ControlName != "NewExpenseReportReportsTab" {
		return errors.New("capture bootstrap new-report target is not allowlisted")
	}
	return nil
}

// SafeSummary returns a log-friendly description. It deliberately includes
// only request-header and cookie names, never their values.
func (p *Profile) SafeSummary() string {
	if p == nil {
		return "capture profile: <nil>"
	}

	headerNames := make([]string, 0, len(p.Session.RequestHeaders))
	for name := range p.Session.RequestHeaders {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)

	cookieNames := make([]string, 0, len(p.Session.Cookies))
	for _, cookie := range p.Session.Cookies {
		if cookie != nil && cookie.Name != "" {
			cookieNames = append(cookieNames, cookie.Name)
		}
	}
	sort.Strings(cookieNames)

	return fmt.Sprintf(
		"capture profile: base=%s endpoint=%s company=%s language=%s channel=%d server-sequence=%d next-client-sequence=%d headers=[%s] cookies=[%s] draft={new-report:%s/%s(%s) set-value:%s/%s(%s) invoke-default:%s/%s(%s) save-and-close:%s/%s(%s)}",
		safeURL(p.Session.BaseURL),
		safeURL(p.Session.EndpointURL),
		p.Session.Company,
		p.Session.Language,
		p.Session.ChannelID,
		p.Session.LastServerSequence,
		p.Session.NextClientSequence,
		strings.Join(headerNames, ","),
		strings.Join(cookieNames, ","),
		p.Draft.NewReport.RootID,
		p.Draft.NewReport.TargetID,
		p.Draft.NewReport.ControlName,
		p.Draft.CreateDraft.SetValue.RootID,
		p.Draft.CreateDraft.SetValue.TargetID,
		p.Draft.CreateDraft.SetValue.ControlName,
		p.Draft.CreateDraft.InvokeDefaultButton.RootID,
		p.Draft.CreateDraft.InvokeDefaultButton.TargetID,
		p.Draft.CreateDraft.InvokeDefaultButton.ControlName,
		p.Draft.SaveAndClose.RootID,
		p.Draft.SaveAndClose.TargetID,
		p.Draft.SaveAndClose.ControlName,
	)
}

// SafeSummary returns a log-friendly bootstrap description. It includes only
// header and cookie names, never captured credential values.
func (p *BootstrapProfile) SafeSummary() string {
	if p == nil {
		return "capture bootstrap profile: <nil>"
	}

	headerNames := make([]string, 0, len(p.Session.RequestHeaders))
	for name := range p.Session.RequestHeaders {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)

	cookieNames := make([]string, 0, len(p.Session.Cookies))
	for _, cookie := range p.Session.Cookies {
		if cookie != nil && cookie.Name != "" {
			cookieNames = append(cookieNames, cookie.Name)
		}
	}
	sort.Strings(cookieNames)

	return fmt.Sprintf(
		"capture bootstrap profile: base=%s endpoint=%s company=%s language=%s channel=%d server-sequence=%d next-client-sequence=%d headers=[%s] cookies=[%s] new-report=%s/%s(%s)",
		safeURL(p.Session.BaseURL),
		safeURL(p.Session.EndpointURL),
		p.Session.Company,
		p.Session.Language,
		p.Session.ChannelID,
		p.Session.LastServerSequence,
		p.Session.NextClientSequence,
		strings.Join(headerNames, ","),
		strings.Join(cookieNames, ","),
		p.NewReport.RootID,
		p.NewReport.TargetID,
		p.NewReport.ControlName,
	)
}

func validateSessionProfile(session SessionProfile) error {
	if err := validateEndpoint(session.BaseURL, session.EndpointURL); err != nil {
		return err
	}
	if strings.TrimSpace(session.Company) == "" {
		return errors.New("capture session company is missing")
	}
	if strings.TrimSpace(session.Language) == "" {
		return errors.New("capture session language is missing")
	}
	if session.ChannelID < 0 {
		return errors.New("capture session channel is invalid")
	}
	if session.LastServerSequence < 0 {
		return errors.New("capture last server sequence is invalid")
	}
	if session.NextClientSequence <= 0 {
		return errors.New("capture next client sequence is invalid")
	}
	if session.NextClientSequence > math.MaxInt64-3 {
		return errors.New("capture client sequence lacks headroom for draft creation")
	}
	return validateCredentials(session.RequestHeaders, session.Cookies)
}

func validateEndpoint(baseRaw, endpointRaw string) error {
	base, err := url.Parse(baseRaw)
	if err != nil || !base.IsAbs() || base.Host == "" {
		return errors.New("capture base URL is invalid")
	}
	endpoint, err := url.Parse(endpointRaw)
	if err != nil || !endpoint.IsAbs() || endpoint.Host == "" {
		return errors.New("capture endpoint URL is invalid")
	}
	if !strings.EqualFold(base.Scheme, "https") || !strings.EqualFold(endpoint.Scheme, "https") {
		return errors.New("capture URLs must use HTTPS")
	}
	if base.User != nil || endpoint.User != nil {
		return errors.New("capture URLs must not contain user information")
	}
	if !strings.EqualFold(base.Scheme, endpoint.Scheme) || !strings.EqualFold(base.Host, endpoint.Host) {
		return errors.New("capture base and endpoint URLs have different origins")
	}
	return nil
}

func validateTarget(label string, target CommandTarget) error {
	if strings.TrimSpace(target.CommandName) == "" || strings.TrimSpace(target.RootID) == "" || strings.TrimSpace(target.TargetID) == "" || strings.TrimSpace(target.ControlName) == "" {
		return fmt.Errorf("capture %s command target is incomplete", label)
	}
	return nil
}

var requiredHeaderNames = []string{
	"ms-dyn-aid",
	"ms-dyn-bsid",
	"ms-dyn-csrftoken",
	"ms-dyn-sid",
}

func validateCredentials(headers http.Header, cookies []*http.Cookie) error {
	credentialErr := &CredentialError{}
	for _, name := range requiredHeaderNames {
		values, ok := headerValues(headers, name)
		if !ok || len(values) == 0 {
			credentialErr.MissingHeaders = append(credentialErr.MissingHeaders, name)
			continue
		}
		for _, value := range values {
			if isRedacted(value) {
				credentialErr.RedactedHeaders = appendUnique(credentialErr.RedactedHeaders, name)
				break
			}
		}
	}

	cookieByName := make(map[string][]string)
	for _, cookie := range cookies {
		if cookie == nil || strings.TrimSpace(cookie.Name) == "" {
			continue
		}
		key := strings.ToLower(cookie.Name)
		cookieByName[key] = append(cookieByName[key], cookie.Value)
		if isRedacted(cookie.Value) {
			credentialErr.RedactedCookies = appendUnique(credentialErr.RedactedCookies, cookie.Name)
		}
	}

	if values := cookieByName["ms-dyn-csrftoken"]; len(values) == 0 {
		credentialErr.MissingCookies = append(credentialErr.MissingCookies, "ms-dyn-csrftoken")
	}

	hasAuthCookie := false
	for name := range cookieByName {
		if strings.HasPrefix(name, strings.ToLower("DynamicsOwinAuth")) {
			hasAuthCookie = true
			break
		}
	}
	if !hasAuthCookie {
		credentialErr.MissingCookies = append(credentialErr.MissingCookies, "DynamicsOwinAuth")
	}

	sort.Strings(credentialErr.MissingHeaders)
	sort.Strings(credentialErr.RedactedHeaders)
	sort.Strings(credentialErr.MissingCookies)
	sort.Strings(credentialErr.RedactedCookies)
	if len(credentialErr.MissingHeaders)+len(credentialErr.RedactedHeaders)+len(credentialErr.MissingCookies)+len(credentialErr.RedactedCookies) != 0 {
		return credentialErr
	}
	return nil
}

func headerValues(headers http.Header, wanted string) ([]string, bool) {
	for name, values := range headers {
		if strings.EqualFold(name, wanted) {
			return values, true
		}
	}
	return nil, false
}

func isRedacted(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return true
	}
	if hasRepeatedAsterisks(value) {
		return true
	}

	words := strings.Fields(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return ' '
	}, value))
	if len(words) == 1 && (words[0] == "secret" || words[0] == "placeholder" || words[0] == "xxxxx") {
		return true
	}
	for _, word := range words {
		if strings.HasPrefix(word, "redact") ||
			strings.HasPrefix(word, "mask") ||
			strings.HasPrefix(word, "remov") ||
			strings.HasPrefix(word, "omit") ||
			strings.HasPrefix(word, "omis") ||
			word == "hidden" {
			return true
		}
	}
	return false
}

func hasRepeatedAsterisks(value string) bool {
	run := 0
	for _, r := range value {
		if r == '*' {
			run++
			if run >= 2 {
				return true
			}
			continue
		}
		run = 0
	}
	return false
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if strings.EqualFold(current, value) {
			return values
		}
	}
	return append(values, value)
}

func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid>"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
