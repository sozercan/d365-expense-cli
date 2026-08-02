package expense

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// submittedReportStatus finds status evidence only in an object that also
// identifies the exact report submitted by the current operation. Dynamics can
// serialize many unrelated workspace rows in one response, so generic status
// discovery is not strong enough for submission confirmation.
func submittedReportStatus(data []byte, reportNumber string) (string, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", false, fmt.Errorf("expense: inspect submit response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return "", false, errors.New("expense: inspect submit response: multiple JSON values")
		}
		return "", false, fmt.Errorf("expense: inspect submit response trailing JSON: %w", err)
	}

	var (
		status string
		score  int
		found  bool
	)
	var walk func(any) error
	walk = func(current any) error {
		switch typed := current.(type) {
		case map[string]any:
			if objectReportNumber(typed) == reportNumber {
				candidate, candidateScore := objectReportStatus(typed)
				if candidateScore > 0 {
					if found && candidate != status {
						return errors.New("expense: submit response contains conflicting statuses for the created report")
					}
					if !found || candidateScore > score {
						status, score, found = candidate, candidateScore, true
					}
				}
			}
			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(value); err != nil {
		return "", false, err
	}
	return status, found, nil
}

func objectReportNumber(object map[string]any) string {
	bestScore := 0
	best := ""
	for name, value := range object {
		text, ok := submissionScalar(value)
		if !ok {
			continue
		}
		score := submissionReportPropertyScore(normalizeSubmissionProperty(name))
		if score > bestScore {
			bestScore, best = score, text
		}
	}
	return best
}

func objectReportStatus(object map[string]any) (string, int) {
	bestScore := 0
	best := ""
	for name, value := range object {
		text, ok := submissionScalar(value)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		score := submissionStatusPropertyScore(normalizeSubmissionProperty(name))
		if score > bestScore {
			bestScore, best = score, text
		}
	}
	return best, bestScore
}

func submissionReportPropertyScore(name string) int {
	switch name {
	case "expnumberfield":
		return 100
	case "expensereportnumberfield":
		return 90
	case "expensereportnumber":
		return 80
	case "reportnumberfield":
		return 70
	case "reportnumber":
		return 60
	default:
		return 0
	}
}

func submissionStatusPropertyScore(name string) int {
	switch name {
	case "expensereportstatusdatamethod":
		return 100
	case "expensereportstatus":
		return 90
	case "reportstatusdatamethod":
		return 80
	case "reportstatus":
		return 70
	case "approvalstatusfield":
		return 60
	default:
		return 0
	}
}

func normalizeSubmissionProperty(name string) string {
	var builder strings.Builder
	builder.Grow(len(name))
	for _, character := range name {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(unicode.ToLower(character))
		}
	}
	return builder.String()
}

func submissionScalar(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	case float64:
		return fmt.Sprint(typed), true
	default:
		return "", false
	}
}

func isDraftStatus(status string) bool {
	normalized := strings.ToLower(strings.TrimSpace(status))
	return normalized == "draft" || normalized == "1"
}
