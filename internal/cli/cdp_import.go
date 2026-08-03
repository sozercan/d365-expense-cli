package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/cdphar"
	"github.com/sozercan/d365-expense-cli/internal/har"
	sessionstore "github.com/sozercan/d365-expense-cli/internal/session"
)

func runSessionImportCDP(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("session import-cdp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "", "session name")
	endpoint := flags.String("cdp", cdphar.DefaultEndpoint, "loopback Chrome DevTools Protocol endpoint")
	wait := flags.Duration("wait", 20*time.Second, "time to record workspace initialization after page load")
	timeout := flags.Duration("timeout", 60*time.Second, "maximum connection, navigation, and capture time")
	force := flags.Bool("force", false, "replace an existing named session")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *name == "" || flags.NArg() != 1 {
		fmt.Fprintln(stderr, "session import-cdp requires --name and exactly one Dynamics Expense workspace URL")
		return 2
	}
	if *wait < 0 || *timeout <= 0 || *wait >= *timeout {
		fmt.Fprintln(stderr, "session import-cdp requires 0 <= --wait < --timeout")
		return 2
	}
	targetURL := flags.Arg(0)
	if err := validateDynamicsCaptureURL(targetURL); err != nil {
		fmt.Fprintf(stderr, "session import-cdp: %v\n", err)
		return 2
	}

	store, err := sessionstore.DefaultStore()
	if err != nil {
		fmt.Fprintf(stderr, "session import-cdp: %v\n", err)
		return 1
	}
	lock, err := store.AcquireLock(*name)
	if err != nil {
		fmt.Fprintf(stderr, "session import-cdp: %v\n", err)
		return 1
	}
	defer func() { _ = lock.Release() }()
	path, err := store.Path(*name)
	if err != nil {
		fmt.Fprintf(stderr, "session import-cdp: %v\n", err)
		return 1
	}
	replaceExisting := false
	if _, err := os.Lstat(path); err == nil {
		if !*force {
			fmt.Fprintf(stderr, "session import-cdp: session %q already exists; use --force to replace it\n", *name)
			return 1
		}
		replaceExisting = true
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "session import-cdp: inspect existing session: %v\n", err)
		return 1
	}

	targetID, _, err := discoverExpenseTarget(*endpoint, targetURL)
	if err != nil {
		fmt.Fprintf(stderr, "session import-cdp: %v\n", err)
		return 1
	}

	fmt.Fprintln(stderr, "importing the authenticated Expense workspace through local CDP; no expense will be created or submitted")
	pageState := &cdphar.DynamicsBootstrapState{}
	archive, err := cdphar.Capture(context.Background(), cdphar.Options{
		Endpoint:               *endpoint,
		URL:                    targetURL,
		Wait:                   *wait,
		Timeout:                *timeout,
		IncludeResponseBodies:  true,
		CreatorName:            "d365-expense",
		CreatorVersion:         "dev",
		DynamicsBootstrap:      pageState,
		ExistingTargetID:       targetID,
		PreserveExistingTarget: true,
	})
	if err != nil {
		fmt.Fprintf(stderr, "session import-cdp: %v\n", err)
		return 1
	}
	if _, err := retainDynamicsProcessMessages(archive, targetURL); err != nil {
		fmt.Fprintf(stderr, "session import-cdp: %v\n", err)
		return 1
	}
	var encoded bytes.Buffer
	if err := har.Save(&encoded, archive); err != nil {
		fmt.Fprintf(stderr, "session import-cdp: encode captured state: %v\n", err)
		return 1
	}
	profile, err := capture.ParseBootstrapWithNewReport(bytes.NewReader(encoded.Bytes()), capture.CommandTarget{
		CommandName: "Click",
		RootID:      pageState.WorkspaceRootID,
		TargetID:    pageState.NewReportID,
		ControlName: "NewExpenseReportReportsTab",
	})
	if err != nil {
		fmt.Fprintf(stderr, "session import-cdp: validate captured state: %v\n", err)
		return 1
	}
	standalone, err := sessionstore.FromBootstrap(profile)
	if err != nil {
		fmt.Fprintf(stderr, "session import-cdp: %v\n", err)
		return 1
	}
	if replaceExisting {
		err = store.Replace(*name, standalone)
	} else {
		err = store.Save(*name, standalone)
	}
	if err != nil {
		fmt.Fprintf(stderr, "session import-cdp: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "imported session %q directly from local CDP\n", *name)
	fmt.Fprintln(stdout, standalone.SafeSummary(*name))
	return 0
}

func discoverExpenseTarget(endpoint, targetRaw string) (string, string, error) {
	base, err := url.Parse(endpoint)
	if err != nil || base.Scheme != "http" || base.Host == "" || base.User != nil {
		return "", "", errors.New("invalid local CDP endpoint")
	}
	discovery := *base
	discovery.Path = "/json/list"
	discovery.RawQuery = ""
	discovery.Fragment = ""
	request, err := http.NewRequest(http.MethodGet, discovery.String(), nil)
	if err != nil {
		return "", "", errors.New("build CDP target discovery request")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", "", fmt.Errorf("discover CDP targets: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", fmt.Errorf("discover CDP targets: HTTP %d", response.StatusCode)
	}
	var targets []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&targets); err != nil {
		return "", "", errors.New("decode CDP targets")
	}
	var wantedOrigin string
	if targetRaw != "" {
		wanted, _ := url.Parse(targetRaw)
		wantedOrigin = normalizedCaptureOrigin(wanted)
	}
	type match struct{ id, url string }
	var matches []match
	for _, candidate := range targets {
		if candidate.Type != "page" || candidate.ID == "" {
			continue
		}
		parsed, err := url.Parse(candidate.URL)
		if err != nil || (wantedOrigin != "" && normalizedCaptureOrigin(parsed) != wantedOrigin) || parsed.Scheme != "https" || !strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".operations.dynamics.com") || parsed.Query().Get("mi") != "ExpenseWorkspace" {
			continue
		}
		matches = append(matches, match{id: candidate.ID, url: candidate.URL})
	}
	if len(matches) != 1 {
		return "", "", fmt.Errorf("expected exactly one open authenticated Expense workspace tab, found %d", len(matches))
	}
	return matches[0].id, matches[0].url, nil
}
