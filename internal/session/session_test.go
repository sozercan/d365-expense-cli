package session

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sozercan/d365-expense-cli/internal/capture"
)

func TestFromBootstrapRoundTripAndDefensiveCopies(t *testing.T) {
	profile := validBootstrapProfile()
	imported, err := FromBootstrap(profile)
	if err != nil {
		t.Fatalf("FromBootstrap() error = %v", err)
	}
	if imported.Version != Version || imported.ImportedAt.IsZero() || imported.UpdatedAt.IsZero() {
		t.Fatalf("timestamps/version were not initialized: %+v", imported.Summary("work"))
	}
	if imported.NewReport.TargetID != "new-report-target" {
		t.Fatalf("NewReport.TargetID = %q", imported.NewReport.TargetID)
	}

	profile.Session.RequestHeaders.Set("ms-dyn-aid", "mutated-header-secret")
	profile.Session.Cookies[0].Value = "mutated-cookie-secret"
	profile.NewReport.TargetID = "mutated-target"
	if got := imported.RequestHeaders().Get("ms-dyn-aid"); got != "header-secret" {
		t.Fatalf("stored header changed through source alias: %q", got)
	}
	if got := cookieValue(imported.HTTPCookies(), "DynamicsOwinAuth"); got != "cookie-secret" {
		t.Fatalf("stored cookie changed through source alias: %q", got)
	}
	if imported.NewReport.TargetID != "new-report-target" {
		t.Fatalf("stored target changed through source alias: %q", imported.NewReport.TargetID)
	}

	roundTrip, err := imported.ToBootstrap()
	if err != nil {
		t.Fatalf("ToBootstrap() error = %v", err)
	}
	if err := roundTrip.Validate(); err != nil {
		t.Fatalf("round-trip Validate() error = %v", err)
	}
	if got := roundTrip.Session.RequestHeaders.Get("ms-dyn-aid"); got != "header-secret" {
		t.Fatalf("round-trip header = %q", got)
	}
	if got := cookieValue(roundTrip.Session.Cookies, "DynamicsOwinAuth"); got != "cookie-secret" {
		t.Fatalf("round-trip cookie = %q", got)
	}
	roundTrip.Session.RequestHeaders.Set("ms-dyn-aid", "changed-again")
	roundTrip.Session.Cookies[0].Value = "changed-again"
	if got := imported.RequestHeaders().Get("ms-dyn-aid"); got != "header-secret" {
		t.Fatalf("stored header changed through result alias: %q", got)
	}
	if got := cookieValue(imported.HTTPCookies(), "DynamicsOwinAuth"); got != "cookie-secret" {
		t.Fatalf("stored cookie changed through result alias: %q", got)
	}
}

func TestFromBootstrapRejectsNilAndInvalidProfiles(t *testing.T) {
	if _, err := FromBootstrap(nil); err == nil {
		t.Fatal("FromBootstrap(nil) error = nil")
	}
	invalid := validBootstrapProfile()
	invalid.NewReport.ControlName = "Submit"
	if _, err := ImportBootstrap(invalid); err == nil || !strings.Contains(err.Error(), "allowlisted") {
		t.Fatalf("ImportBootstrap(invalid) error = %v", err)
	}
}

func TestApplyBootstrapPreservesImportTimeAndUpdatesState(t *testing.T) {
	session, err := FromBootstrap(validBootstrapProfile())
	if err != nil {
		t.Fatal(err)
	}
	importedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Nanosecond)
	session.ImportedAt = importedAt
	session.UpdatedAt = importedAt

	updated := validBootstrapProfile()
	updated.Session.LastServerSequence = 99
	updated.Session.NextClientSequence = 200
	updated.Session.Cookies[0].Value = "rotated-cookie"
	if err := session.ApplyBootstrap(updated); err != nil {
		t.Fatalf("ApplyBootstrap() error = %v", err)
	}
	if !session.ImportedAt.Equal(importedAt) {
		t.Fatalf("ImportedAt = %v, want %v", session.ImportedAt, importedAt)
	}
	if session.UpdatedAt.Before(importedAt) {
		t.Fatalf("UpdatedAt = %v before import", session.UpdatedAt)
	}
	if session.LastServerSequence != 99 || session.NextClientSequence != 200 {
		t.Fatalf("sequence state = %d/%d", session.LastServerSequence, session.NextClientSequence)
	}
	if got := cookieValue(session.HTTPCookies(), "DynamicsOwinAuth"); got != "rotated-cookie" {
		t.Fatalf("rotated cookie = %q", got)
	}
}

func TestSessionValidateRejectsMalformedState(t *testing.T) {
	valid, err := fromBootstrapAt(validBootstrapProfile(), time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Session)
		want   string
	}{
		{name: "version", mutate: func(s *Session) { s.Version++ }, want: "version"},
		{name: "missing import time", mutate: func(s *Session) { s.ImportedAt = time.Time{} }, want: "import time"},
		{name: "missing update time", mutate: func(s *Session) { s.UpdatedAt = time.Time{} }, want: "update time"},
		{name: "time reversal", mutate: func(s *Session) { s.UpdatedAt = s.ImportedAt.Add(-time.Second) }, want: "precedes"},
		{name: "http URL", mutate: func(s *Session) { s.BaseURL = "http://tenant.operations.dynamics.com" }, want: "HTTPS"},
		{name: "different endpoint origin", mutate: func(s *Session) {
			s.EndpointURL = "https://other.operations.dynamics.com/Services/ReliableCommunicationManager.svc/ProcessMessages"
		}, want: "different origins"},
		{name: "company control", mutate: func(s *Session) { s.Company = "USMF\n" }, want: "company"},
		{name: "negative channel", mutate: func(s *Session) { s.ChannelID = -1 }, want: "channel"},
		{name: "negative server sequence", mutate: func(s *Session) { s.LastServerSequence = -1 }, want: "server sequence"},
		{name: "zero client sequence", mutate: func(s *Session) { s.NextClientSequence = 0 }, want: "client sequence"},
		{name: "client sequence headroom", mutate: func(s *Session) { s.NextClientSequence = math.MaxInt64 }, want: "headroom"},
		{name: "duplicate header", mutate: func(s *Session) {
			s.Headers = append(s.Headers, Header{Name: strings.ToUpper(s.Headers[0].Name), Values: []string{"duplicate"}})
		}, want: "duplicate header"},
		{name: "header injection", mutate: func(s *Session) { s.Headers[0].Values[0] = "bad\r\nvalue" }, want: "invalid value"},
		{name: "missing credential header", mutate: func(s *Session) { s.Headers = removeHeader(s.Headers, "ms-dyn-sid") }, want: "missing headers"},
		{name: "redacted credential", mutate: func(s *Session) { setHeader(s.Headers, "ms-dyn-sid", "<redacted>") }, want: "redacted headers"},
		{name: "invalid cookie", mutate: func(s *Session) { s.Cookies[0].Name = "bad cookie" }, want: "cookie"},
		{name: "foreign cookie domain", mutate: func(s *Session) { s.Cookies[0].Domain = "example.com" }, want: "domain"},
		{name: "duplicate cookie", mutate: func(s *Session) { s.Cookies = append(s.Cookies, s.Cookies[0]) }, want: "duplicate cookie"},
		{name: "missing auth cookie", mutate: func(s *Session) { s.Cookies = removeCookie(s.Cookies, "DynamicsOwinAuth") }, want: "DynamicsOwinAuth"},
		{name: "wrong command", mutate: func(s *Session) { s.NewReport.CommandName = "Submit" }, want: "allowlisted"},
		{name: "missing target", mutate: func(s *Session) { s.NewReport.TargetID = "" }, want: "target ID"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneSession(t, valid)
			test.mutate(candidate)
			err := candidate.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestSafeSummaryAndFormattingNeverExposeCredentialsOrQuery(t *testing.T) {
	session, err := FromBootstrap(validBootstrapProfile())
	if err != nil {
		t.Fatal(err)
	}
	outputs := []string{
		session.SafeSummary("work"),
		session.Summary("work").String(),
		fmt.Sprint(*session),
		fmt.Sprintf("%#v", *session),
	}
	for _, output := range outputs {
		for _, forbidden := range []string{"header-secret", "cookie-secret", "cmp=USMF", "lng=en-us", "new-report-target", "workspace-root"} {
			if strings.Contains(output, forbidden) {
				t.Errorf("safe output contains %q: %s", forbidden, output)
			}
		}
		for _, expected := range []string{"ms-dyn-aid", "DynamicsOwinAuth", "NewExpenseReportReportsTab", "tenant.operations.dynamics.com"} {
			if !strings.Contains(strings.ToLower(output), strings.ToLower(expected)) {
				t.Errorf("safe output missing %q: %s", expected, output)
			}
		}
	}
}

func TestRequestHeadersAndHTTPCookiesReturnCopies(t *testing.T) {
	session, err := FromBootstrap(validBootstrapProfile())
	if err != nil {
		t.Fatal(err)
	}
	headers := session.RequestHeaders()
	headers.Set("ms-dyn-aid", "mutated")
	cookies := session.HTTPCookies()
	cookies[0].Value = "mutated"
	if got := session.RequestHeaders().Get("ms-dyn-aid"); got != "header-secret" {
		t.Fatalf("header aliasing: %q", got)
	}
	if got := cookieValue(session.HTTPCookies(), "DynamicsOwinAuth"); got != "cookie-secret" {
		t.Fatalf("cookie aliasing: %q", got)
	}
}

func validBootstrapProfile() *capture.BootstrapProfile {
	headers := http.Header{
		"Accept":           []string{"application/json"},
		"Content-Type":     []string{"application/json; charset=UTF-8"},
		"Origin":           []string{"https://tenant.operations.dynamics.com"},
		"Referer":          []string{"https://tenant.operations.dynamics.com/?cmp=USMF"},
		"X-Requested-With": []string{"XMLHttpRequest"},
		"ms-dyn-aid":       []string{"header-secret"},
		"ms-dyn-bsid":      []string{"header-secret"},
		"ms-dyn-csrftoken": []string{"header-secret"},
		"ms-dyn-sid":       []string{"header-secret"},
	}
	return &capture.BootstrapProfile{
		Session: capture.SessionProfile{
			BaseURL:        "https://tenant.operations.dynamics.com",
			EndpointURL:    "https://tenant.operations.dynamics.com/Services/ReliableCommunicationManager.svc/ProcessMessages?cmp=USMF&lng=en-us",
			RequestHeaders: headers,
			Cookies: []*http.Cookie{
				{Name: "DynamicsOwinAuth", Value: "cookie-secret", Domain: "tenant.operations.dynamics.com", Path: "/", Secure: true, HttpOnly: true},
				{Name: "backend-affinity", Value: "affinity-secret", Domain: ".operations.dynamics.com", Path: "/", Secure: true},
				{Name: "ms-dyn-csrftoken", Value: "csrf-cookie-secret", Domain: "tenant.operations.dynamics.com", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteNoneMode},
			},
			Company:            "USMF",
			Language:           "en-us",
			ChannelID:          7,
			LastServerSequence: 41,
			NextClientSequence: 101,
		},
		NewReport: capture.CommandTarget{
			CommandName: "Click",
			RootID:      "workspace-root",
			TargetID:    "new-report-target",
			ControlName: "NewExpenseReportReportsTab",
		},
	}
}

func cloneSession(t *testing.T, source *Session) *Session {
	t.Helper()
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var result Session
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return &result
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie != nil && strings.EqualFold(cookie.Name, name) {
			return cookie
		}
	}
	return nil
}

func cookieValue(cookies []*http.Cookie, name string) string {
	for _, cookie := range cookies {
		if cookie != nil && strings.EqualFold(cookie.Name, name) {
			return cookie.Value
		}
	}
	return ""
}

func setHeader(headers []Header, name, value string) {
	for index := range headers {
		if strings.EqualFold(headers[index].Name, name) {
			headers[index].Values = []string{value}
			return
		}
	}
}

func removeHeader(headers []Header, name string) []Header {
	result := make([]Header, 0, len(headers))
	for _, header := range headers {
		if !strings.EqualFold(header.Name, name) {
			result = append(result, header)
		}
	}
	return result
}

func removeCookie(cookies []Cookie, name string) []Cookie {
	result := make([]Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		if !strings.EqualFold(cookie.Name, name) {
			result = append(result, cookie)
		}
	}
	return result
}

func TestCookieConversionPreservesTransportFields(t *testing.T) {
	expires := time.Date(2026, 9, 1, 12, 0, 0, 0, time.FixedZone("offset", -7*60*60))
	profile := validBootstrapProfile()
	profile.Session.Cookies[0].Expires = expires
	profile.Session.Cookies[0].MaxAge = 3600
	profile.Session.Cookies[0].SameSite = http.SameSiteLaxMode
	profile.Session.Cookies[0].Partitioned = true
	profile.Session.Cookies[0].Secure = true

	session, err := FromBootstrap(profile)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := session.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	got := findCookie(roundTrip.Session.Cookies, "DynamicsOwinAuth")
	want := profile.Session.Cookies[0]
	if got == nil {
		t.Fatal("round-trip DynamicsOwinAuth cookie is missing")
	}
	if got.Name != want.Name || got.Value != want.Value || got.Domain != want.Domain || got.Path != want.Path ||
		got.MaxAge != want.MaxAge || got.SameSite != want.SameSite || got.Partitioned != want.Partitioned || !got.Expires.Equal(want.Expires) {
		t.Fatalf("cookie round trip mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestHeaderAndCookieOrderingIsDeterministic(t *testing.T) {
	first, err := FromBootstrap(validBootstrapProfile())
	if err != nil {
		t.Fatal(err)
	}
	secondProfile := validBootstrapProfile()
	secondProfile.Session.RequestHeaders = http.Header{}
	for _, name := range []string{"ms-dyn-sid", "Referer", "ms-dyn-aid", "Accept", "Origin", "ms-dyn-csrftoken", "Content-Type", "X-Requested-With", "ms-dyn-bsid"} {
		secondProfile.Session.RequestHeaders[name] = append([]string(nil), validBootstrapProfile().Session.RequestHeaders[name]...)
	}
	secondProfile.Session.Cookies[0], secondProfile.Session.Cookies[2] = secondProfile.Session.Cookies[2], secondProfile.Session.Cookies[0]
	second, err := FromBootstrap(secondProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Headers, second.Headers) {
		t.Fatalf("header ordering differs:\n%#v\n%#v", first.Headers, second.Headers)
	}
	if !reflect.DeepEqual(first.Cookies, second.Cookies) {
		t.Fatalf("cookie ordering differs:\n%#v\n%#v", first.Cookies, second.Cookies)
	}
}
