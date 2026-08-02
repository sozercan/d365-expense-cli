package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/cdphar"
	"github.com/sozercan/d365-expense-cli/internal/har"
)

const (
	defaultDraftCaptureWait     = 10 * time.Second
	defaultDraftCaptureTimeout  = 45 * time.Second
	dynamicsProcessMessagesPath = "/Services/ReliableCommunicationManager.svc/ProcessMessages"
)

var captureDraftHAR = cdphar.Capture

func runCaptureDraft(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("capture-draft", flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("cdp", cdphar.DefaultEndpoint, "Chrome DevTools Protocol endpoint")
	outPath := flags.String("out", "", "private raw HAR output path")
	wait := flags.Duration("wait", defaultDraftCaptureWait, "time to record workspace initialization after page load")
	timeout := flags.Duration("timeout", defaultDraftCaptureTimeout, "maximum time for connection, navigation, and capture")
	allowRemote := flags.Bool("allow-remote-cdp", false, "allow a non-loopback exact browser WebSocket endpoint")
	force := flags.Bool("force", false, "replace an existing regular output file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "capture-draft requires exactly one Dynamics expense workspace URL")
		return 2
	}
	if strings.TrimSpace(*outPath) == "" {
		fmt.Fprintln(stderr, "capture-draft requires --out")
		return 2
	}
	if *outPath == "-" {
		fmt.Fprintln(stderr, "capture-draft raw HAR output to stdout is disabled")
		return 2
	}
	if *wait < 0 {
		fmt.Fprintln(stderr, "capture-draft requires a non-negative --wait")
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "capture-draft requires a positive --timeout")
		return 2
	}
	if *wait >= *timeout {
		fmt.Fprintln(stderr, "capture-draft requires --wait to be shorter than --timeout")
		return 2
	}
	if runtime.GOOS == "windows" {
		fmt.Fprintln(stderr, "capture-draft: owner-only raw HAR writes are unsupported on Windows")
		return 1
	}

	targetURL := flags.Arg(0)
	if err := validateDynamicsCaptureURL(targetURL); err != nil {
		fmt.Fprintf(stderr, "capture-draft: %v\n", err)
		return 2
	}
	if err := validateCaptureOutput(*outPath, *force); err != nil {
		fmt.Fprintf(stderr, "capture-draft: %v\n", err)
		return 1
	}

	fmt.Fprintln(stderr, "capture-draft opens a new browser tab and records the authenticated Expense workspace bootstrap.")
	fmt.Fprintln(stderr, "No expense report needs to be created; do not click Submit.")
	fmt.Fprintf(stderr, "Recording continues for %s after the page loads.\n", wait.String())

	archive, err := captureDraftHAR(context.Background(), cdphar.Options{
		Endpoint:              *endpoint,
		URL:                   targetURL,
		Wait:                  *wait,
		Timeout:               *timeout,
		IncludeResponseBodies: true,
		AllowRemoteEndpoint:   *allowRemote,
		CreatorName:           "d365-expense",
		CreatorVersion:        "dev",
	})
	if err != nil {
		fmt.Fprintf(stderr, "capture-draft: %v\n", err)
		return 1
	}

	entryCount, err := retainDynamicsProcessMessages(archive, targetURL)
	if err != nil {
		fmt.Fprintf(stderr, "capture-draft: %v\n", err)
		return 1
	}

	var encoded bytes.Buffer
	if err := har.Save(&encoded, archive); err != nil {
		fmt.Fprintf(stderr, "capture-draft: encode captured HAR: %v\n", err)
		return 1
	}
	profile, err := capture.ParseBootstrap(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		fmt.Fprintf(stderr, "capture-draft: captured traffic does not contain an executable Expense workspace bootstrap: %v\n", err)
		return 1
	}
	if err := har.SaveFile(*outPath, archive); err != nil {
		fmt.Fprintf(stderr, "capture-draft: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "captured and validated %d Dynamics requests -> %s\n", entryCount, *outPath)
	fmt.Fprintln(stdout, profile.SafeSummary())
	return 0
}

func validateDynamicsCaptureURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return errors.New("invalid Dynamics expense workspace URL")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("Dynamics expense workspace URL must use https")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if !strings.HasSuffix(host, ".operations.dynamics.com") {
		return errors.New("Dynamics expense workspace URL must be under operations.dynamics.com")
	}
	if parsed.Fragment != "" {
		return errors.New("Dynamics expense workspace URL must not contain a fragment")
	}
	return nil
}

func validateCaptureOutput(path string, force bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect output path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("output path must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return errors.New("output path exists and is not a regular file")
	}
	if !force {
		return errors.New("output path already exists; use --force to replace it")
	}
	return nil
}

func normalizedCaptureOrigin(value *url.URL) string {
	if value == nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(value.Hostname(), "."))
	port := value.Port()
	if port == "" {
		switch strings.ToLower(value.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	if host == "" || port == "" {
		return ""
	}
	return strings.ToLower(value.Scheme) + "://" + net.JoinHostPort(host, port)
}

func retainDynamicsProcessMessages(archive *har.Archive, targetRaw string) (int, error) {
	if archive == nil {
		return 0, errors.New("captured HAR is nil")
	}
	target, err := url.Parse(targetRaw)
	if err != nil {
		return 0, errors.New("invalid capture target URL")
	}
	targetOrigin := normalizedCaptureOrigin(target)

	filtered := make([]har.Entry, 0, len(archive.Log.Entries))
	for _, entry := range archive.Log.Entries {
		if !strings.EqualFold(entry.Request.Method, "POST") {
			continue
		}
		requestURL, err := url.Parse(entry.Request.URL)
		if err != nil {
			continue
		}
		origin := normalizedCaptureOrigin(requestURL)
		if origin != targetOrigin || requestURL.Path != dynamicsProcessMessagesPath {
			continue
		}
		entry.PageRef = ""
		entry.ServerIPAddress = ""
		entry.Connection = ""
		entry.Comment = ""
		entry.ResourceType = ""
		entry.Initiator = nil
		entry.Request.QueryString = nil
		entry.Request.Comment = ""
		entry.Response.Headers = nil
		entry.Response.Cookies = nil
		entry.Response.Comment = ""
		filtered = append(filtered, entry)
	}
	if len(filtered) == 0 {
		return 0, errors.New("capture contains no same-origin Dynamics ProcessMessages requests")
	}
	archive.Log.Entries = filtered
	archive.Log.Pages = nil
	return len(filtered), nil
}
