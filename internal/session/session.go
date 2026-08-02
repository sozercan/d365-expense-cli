// Package session stores the minimal authenticated Dynamics 365 state needed
// to create expense drafts without a running browser. The canonical store is
// rooted at os.UserConfigDir()/d365-expense; DefaultStore also supports the
// legacy msexpense environment and home-directory store as explicit,
// non-merging compatibility fallbacks. Session files are plaintext secrets;
// callers should expose only Summary values in logs.
package session

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/sozercan/d365-expense-cli/internal/capture"
)

type Status string

const (
	StatusReady      Status = "ready"
	StatusInProgress Status = "in_progress"
	StatusUncertain  Status = "uncertain"
	StatusExpired    Status = "expired"

	// Version is the current persisted session schema version.
	Version = 1

	maxURLLength       = 16 << 10
	maxHeaders         = 64
	maxHeaderValues    = 16
	maxHeaderValueSize = 64 << 10
	maxCookies         = 256
	maxCookieValueSize = 64 << 10
	maxIdentifierSize  = 4 << 10
)

// Session is the versioned, minimal Dynamics workspace state persisted by the
// standalone CLI. Header and cookie values are credentials.
type Session struct {
	Version            int           `json:"version"`
	Status             Status        `json:"status"`
	ImportedAt         time.Time     `json:"importedAt"`
	UpdatedAt          time.Time     `json:"updatedAt"`
	BaseURL            string        `json:"baseUrl"`
	EndpointURL        string        `json:"endpointUrl"`
	Company            string        `json:"company"`
	Language           string        `json:"language"`
	ChannelID          int64         `json:"channelId"`
	LastServerSequence int64         `json:"lastServerSequence"`
	NextClientSequence int64         `json:"nextClientSequence"`
	Headers            []Header      `json:"headers"`
	Cookies            []Cookie      `json:"cookies"`
	NewReport          CommandTarget `json:"newReport"`
}

// Header is one persisted HTTP request header. Values can contain credentials.
type Header struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// Cookie is the serializable subset of http.Cookie required for replay.
type Cookie struct {
	Name        string        `json:"name"`
	Value       string        `json:"value"`
	Path        string        `json:"path,omitempty"`
	Domain      string        `json:"domain,omitempty"`
	Expires     *time.Time    `json:"expires,omitempty"`
	MaxAge      int           `json:"maxAge,omitempty"`
	Secure      bool          `json:"secure,omitempty"`
	HTTPOnly    bool          `json:"httpOnly,omitempty"`
	SameSite    http.SameSite `json:"sameSite,omitempty"`
	Partitioned bool          `json:"partitioned,omitempty"`
}

// CommandTarget identifies the allowlisted New expense report control.
type CommandTarget struct {
	CommandName string `json:"commandName"`
	RootID      string `json:"rootId"`
	TargetID    string `json:"targetId"`
	ControlName string `json:"controlName"`
}

// Summary contains only non-secret session metadata and credential names.
type Summary struct {
	Name                 string    `json:"name,omitempty"`
	Version              int       `json:"version"`
	Status               Status    `json:"status"`
	ImportedAt           time.Time `json:"importedAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
	Origin               string    `json:"origin"`
	EndpointPath         string    `json:"endpointPath"`
	Company              string    `json:"company"`
	Language             string    `json:"language"`
	ChannelID            int64     `json:"channelId"`
	LastServerSequence   int64     `json:"lastServerSequence"`
	NextClientSequence   int64     `json:"nextClientSequence"`
	HeaderNames          []string  `json:"headerNames"`
	CookieNames          []string  `json:"cookieNames"`
	NewReportControlName string    `json:"newReportControlName"`
}

// FromBootstrap converts a validated capture bootstrap into a standalone
// session, taking defensive copies of every credential value.
func FromBootstrap(profile *capture.BootstrapProfile) (*Session, error) {
	return fromBootstrapAt(profile, time.Now().UTC())
}

// ImportBootstrap is an alias for FromBootstrap for import-oriented callers.
func ImportBootstrap(profile *capture.BootstrapProfile) (*Session, error) {
	return FromBootstrap(profile)
}

func fromBootstrapAt(profile *capture.BootstrapProfile, now time.Time) (*Session, error) {
	if profile == nil {
		return nil, errors.New("session: capture bootstrap profile is nil")
	}
	if err := profile.Validate(); err != nil {
		return nil, fmt.Errorf("session: validate capture bootstrap profile: %w", err)
	}
	now = now.UTC()
	result := &Session{
		Version:            Version,
		Status:             StatusReady,
		ImportedAt:         now,
		UpdatedAt:          now,
		BaseURL:            profile.Session.BaseURL,
		EndpointURL:        profile.Session.EndpointURL,
		Company:            profile.Session.Company,
		Language:           profile.Session.Language,
		ChannelID:          profile.Session.ChannelID,
		LastServerSequence: profile.Session.LastServerSequence,
		NextClientSequence: profile.Session.NextClientSequence,
		Headers:            headersFromHTTP(profile.Session.RequestHeaders),
		Cookies:            cookiesFromHTTP(profile.Session.Cookies),
		NewReport:          commandTargetFromCapture(profile.NewReport),
	}
	if err := result.Validate(); err != nil {
		return nil, fmt.Errorf("session: convert capture bootstrap profile: %w", err)
	}
	return result, nil
}

// ApplyBootstrap replaces replay state and credentials while preserving the
// original import time. It is useful when persisting sequence/cookie progress.
func (session *Session) ApplyBootstrap(profile *capture.BootstrapProfile) error {
	if session == nil {
		return errors.New("session is nil")
	}
	if profile == nil {
		return errors.New("session: capture bootstrap profile is nil")
	}
	if err := profile.Validate(); err != nil {
		return fmt.Errorf("session: validate capture bootstrap profile: %w", err)
	}

	importedAt := session.ImportedAt
	if importedAt.IsZero() {
		importedAt = time.Now().UTC()
	}
	updatedAt := time.Now().UTC()
	if updatedAt.Before(importedAt) {
		updatedAt = importedAt
	}
	status := session.Status
	if status == "" {
		status = StatusReady
	}
	updated := &Session{
		Version:            Version,
		Status:             status,
		ImportedAt:         importedAt,
		UpdatedAt:          updatedAt,
		BaseURL:            profile.Session.BaseURL,
		EndpointURL:        profile.Session.EndpointURL,
		Company:            profile.Session.Company,
		Language:           profile.Session.Language,
		ChannelID:          profile.Session.ChannelID,
		LastServerSequence: profile.Session.LastServerSequence,
		NextClientSequence: profile.Session.NextClientSequence,
		Headers:            headersFromHTTP(profile.Session.RequestHeaders),
		Cookies:            cookiesFromHTTP(profile.Session.Cookies),
		NewReport:          commandTargetFromCapture(profile.NewReport),
	}
	if err := updated.Validate(); err != nil {
		return fmt.Errorf("session: apply capture bootstrap profile: %w", err)
	}
	*session = *updated
	return nil
}

// Bootstrap returns a defensive capture profile suitable for constructing a
// Dynamics client.
func (session *Session) Bootstrap() (*capture.BootstrapProfile, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	return session.bootstrapUnchecked(), nil
}

// ToBootstrap is an alias for Bootstrap.
func (session *Session) ToBootstrap() (*capture.BootstrapProfile, error) {
	return session.Bootstrap()
}

// Validate verifies both the persisted schema and the replay safety rules.
func (session *Session) Validate() error {
	if session == nil {
		return errors.New("session is nil")
	}
	if session.Version != Version {
		return fmt.Errorf("session version must be %d", Version)
	}
	switch session.Status {
	case StatusReady, StatusInProgress, StatusUncertain, StatusExpired:
	default:
		return errors.New("session status is invalid")
	}
	if session.ImportedAt.IsZero() {
		return errors.New("session import time is required")
	}
	if session.UpdatedAt.IsZero() {
		return errors.New("session update time is required")
	}
	if session.UpdatedAt.Before(session.ImportedAt) {
		return errors.New("session update time precedes import time")
	}
	if len(session.BaseURL) > maxURLLength || len(session.EndpointURL) > maxURLLength {
		return errors.New("session URL is too long")
	}
	if err := validateShortText("company", session.Company); err != nil {
		return err
	}
	if err := validateShortText("language", session.Language); err != nil {
		return err
	}
	if session.ChannelID < 0 {
		return errors.New("session channel is invalid")
	}
	if session.LastServerSequence < 0 {
		return errors.New("session last server sequence is invalid")
	}
	if session.NextClientSequence <= 0 {
		return errors.New("session next client sequence is invalid")
	}
	if session.NextClientSequence > math.MaxInt64-3 {
		return errors.New("session client sequence lacks headroom for draft creation")
	}
	if err := validateHeaders(session.Headers); err != nil {
		return err
	}
	base, err := url.Parse(session.BaseURL)
	if err != nil || base.Hostname() == "" {
		return errors.New("session base URL is invalid")
	}
	if err := validateCookies(session.Cookies, base.Hostname()); err != nil {
		return err
	}
	if err := validateCommandTarget(session.NewReport); err != nil {
		return err
	}

	profile := session.bootstrapUnchecked()
	if err := profile.Validate(); err != nil {
		return fmt.Errorf("session capture state is invalid: %w", err)
	}
	return nil
}

// RequestHeaders returns a defensive http.Header copy.
func (session *Session) RequestHeaders() http.Header {
	if session == nil {
		return nil
	}
	result := make(http.Header, len(session.Headers))
	for _, header := range session.Headers {
		name := http.CanonicalHeaderKey(header.Name)
		result[name] = append([]string(nil), header.Values...)
	}
	return result
}

// HTTPCookies returns defensive http.Cookie copies.
func (session *Session) HTTPCookies() []*http.Cookie {
	if session == nil {
		return nil
	}
	result := make([]*http.Cookie, 0, len(session.Cookies))
	for _, cookie := range session.Cookies {
		result = append(result, cookie.HTTP())
	}
	return result
}

// HTTP converts a persisted cookie into a defensive http.Cookie value.
func (cookie Cookie) HTTP() *http.Cookie {
	result := &http.Cookie{
		Name:        cookie.Name,
		Value:       cookie.Value,
		Path:        cookie.Path,
		Domain:      cookie.Domain,
		MaxAge:      cookie.MaxAge,
		Secure:      cookie.Secure,
		HttpOnly:    cookie.HTTPOnly,
		SameSite:    cookie.SameSite,
		Partitioned: cookie.Partitioned,
	}
	if cookie.Expires != nil {
		result.Expires = cookie.Expires.UTC()
	}
	return result
}

// Summary returns a credential-free description of the session.
func (session *Session) Summary(name string) Summary {
	if session == nil {
		return Summary{Name: name}
	}
	headerNames := make([]string, 0, len(session.Headers))
	for _, header := range session.Headers {
		headerNames = append(headerNames, header.Name)
	}
	cookieNames := make([]string, 0, len(session.Cookies))
	for _, cookie := range session.Cookies {
		cookieNames = append(cookieNames, cookie.Name)
	}
	sort.Strings(headerNames)
	sort.Strings(cookieNames)

	origin, endpointPath := safeLocations(session.BaseURL, session.EndpointURL)
	return Summary{
		Name:                 name,
		Version:              session.Version,
		Status:               session.Status,
		ImportedAt:           session.ImportedAt,
		UpdatedAt:            session.UpdatedAt,
		Origin:               origin,
		EndpointPath:         endpointPath,
		Company:              session.Company,
		Language:             session.Language,
		ChannelID:            session.ChannelID,
		LastServerSequence:   session.LastServerSequence,
		NextClientSequence:   session.NextClientSequence,
		HeaderNames:          headerNames,
		CookieNames:          cookieNames,
		NewReportControlName: session.NewReport.ControlName,
	}
}

// SafeSummary returns a log-friendly description without credential values.
func (session *Session) SafeSummary(name string) string {
	return session.Summary(name).String()
}

// String deliberately renders only non-secret metadata.
func (session Session) String() string { return (&session).SafeSummary("") }

// GoString deliberately renders only non-secret metadata.
func (session Session) GoString() string { return (&session).SafeSummary("") }

// String formats a summary without secrets or URL query parameters.
func (summary Summary) String() string {
	name := summary.Name
	if name == "" {
		name = "<unnamed>"
	}
	return fmt.Sprintf(
		"session %s: version=%d status=%s imported=%s updated=%s origin=%s endpoint=%s company=%s language=%s channel=%d server-sequence=%d next-client-sequence=%d headers=[%s] cookies=[%s] new-report=%s",
		name,
		summary.Version,
		summary.Status,
		summary.ImportedAt.UTC().Format(time.RFC3339),
		summary.UpdatedAt.UTC().Format(time.RFC3339),
		summary.Origin,
		summary.EndpointPath,
		summary.Company,
		summary.Language,
		summary.ChannelID,
		summary.LastServerSequence,
		summary.NextClientSequence,
		strings.Join(summary.HeaderNames, ","),
		strings.Join(summary.CookieNames, ","),
		summary.NewReportControlName,
	)
}

func (session *Session) bootstrapUnchecked() *capture.BootstrapProfile {
	return &capture.BootstrapProfile{
		Session: capture.SessionProfile{
			BaseURL:            session.BaseURL,
			EndpointURL:        session.EndpointURL,
			RequestHeaders:     session.RequestHeaders(),
			Cookies:            session.HTTPCookies(),
			Company:            session.Company,
			Language:           session.Language,
			ChannelID:          session.ChannelID,
			LastServerSequence: session.LastServerSequence,
			NextClientSequence: session.NextClientSequence,
		},
		NewReport: capture.CommandTarget{
			CommandName: session.NewReport.CommandName,
			RootID:      session.NewReport.RootID,
			TargetID:    session.NewReport.TargetID,
			ControlName: session.NewReport.ControlName,
		},
	}
}

func headersFromHTTP(headers http.Header) []Header {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	result := make([]Header, 0, len(names))
	for _, name := range names {
		result = append(result, Header{Name: http.CanonicalHeaderKey(name), Values: append([]string(nil), headers[name]...)})
	}
	return result
}

func cookiesFromHTTP(cookies []*http.Cookie) []Cookie {
	result := make([]Cookie, 0, len(cookies))
	for _, captured := range cookies {
		if captured == nil {
			continue
		}
		cookie := Cookie{
			Name:        captured.Name,
			Value:       captured.Value,
			Path:        captured.Path,
			Domain:      captured.Domain,
			MaxAge:      captured.MaxAge,
			Secure:      captured.Secure,
			HTTPOnly:    captured.HttpOnly,
			SameSite:    captured.SameSite,
			Partitioned: captured.Partitioned,
		}
		if !captured.Expires.IsZero() {
			expires := captured.Expires.UTC()
			cookie.Expires = &expires
		}
		result = append(result, cookie)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return cookieKey(result[i]) < cookieKey(result[j])
	})
	return result
}

func commandTargetFromCapture(target capture.CommandTarget) CommandTarget {
	return CommandTarget{
		CommandName: target.CommandName,
		RootID:      target.RootID,
		TargetID:    target.TargetID,
		ControlName: target.ControlName,
	}
}

func validateHeaders(headers []Header) error {
	if len(headers) == 0 {
		return errors.New("session headers are required")
	}
	if len(headers) > maxHeaders {
		return errors.New("session contains too many headers")
	}
	seen := make(map[string]struct{}, len(headers))
	for index, header := range headers {
		if !validHeaderName(header.Name) {
			return fmt.Errorf("session header %d has an invalid name", index)
		}
		key := strings.ToLower(header.Name)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("session contains duplicate header %q", header.Name)
		}
		seen[key] = struct{}{}
		if len(header.Values) == 0 || len(header.Values) > maxHeaderValues {
			return fmt.Errorf("session header %q has an invalid value count", header.Name)
		}
		for _, value := range header.Values {
			if value == "" || len(value) > maxHeaderValueSize || invalidHTTPValue(value) {
				return fmt.Errorf("session header %q has an invalid value", header.Name)
			}
		}
	}
	return nil
}

func validateCookies(cookies []Cookie, requestHost string) error {
	if len(cookies) == 0 {
		return errors.New("session cookies are required")
	}
	if len(cookies) > maxCookies {
		return errors.New("session contains too many cookies")
	}
	seen := make(map[string]struct{}, len(cookies))
	for index, cookie := range cookies {
		if len(cookie.Value) > maxCookieValueSize {
			return fmt.Errorf("session cookie %d value is too large", index)
		}
		candidate := cookie.HTTP()
		if err := candidate.Valid(); err != nil {
			return fmt.Errorf("session cookie %d is invalid: %w", index, err)
		}
		if !cookieDomainMatchesHost(cookie.Domain, requestHost) {
			return fmt.Errorf("session cookie %q domain does not match the Dynamics origin", cookie.Name)
		}
		key := cookieKey(cookie)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("session contains duplicate cookie %q", cookie.Name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateCommandTarget(target CommandTarget) error {
	values := []struct {
		label string
		value string
	}{
		{"command name", target.CommandName},
		{"root ID", target.RootID},
		{"target ID", target.TargetID},
		{"control name", target.ControlName},
	}
	for _, item := range values {
		if strings.TrimSpace(item.value) == "" || len(item.value) > maxIdentifierSize || containsControl(item.value) {
			return fmt.Errorf("session new-report %s is invalid", item.label)
		}
	}
	if target.CommandName != "Click" || target.ControlName != "NewExpenseReportReportsTab" {
		return errors.New("session new-report target is not allowlisted")
	}
	return nil
}

func validateShortText(label, value string) error {
	if strings.TrimSpace(value) == "" || len(value) > 256 || containsControl(value) {
		return fmt.Errorf("session %s is invalid", label)
	}
	return nil
}

func validHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') {
			continue
		}
		switch character {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func invalidHTTPValue(value string) bool {
	for _, character := range value {
		if character == '\r' || character == '\n' || character == 0 || (unicode.IsControl(character) && character != '\t') {
			return true
		}
	}
	return false
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func cookieDomainMatchesHost(domain, host string) bool {
	domain = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
	host = strings.ToLower(strings.TrimSpace(host))
	if domain == "" {
		return true
	}
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func cookieKey(cookie Cookie) string {
	return strings.ToLower(strings.TrimPrefix(cookie.Domain, ".")) + "\x00" + cookie.Path + "\x00" + cookie.Name
}

func safeLocations(baseRaw, endpointRaw string) (string, string) {
	origin := "<invalid>"
	if parsed, err := url.Parse(baseRaw); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		origin = strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
	}
	endpointPath := "<invalid>"
	if parsed, err := url.Parse(endpointRaw); err == nil && parsed.Path != "" {
		endpointPath = parsed.EscapedPath()
	}
	return origin, endpointPath
}
