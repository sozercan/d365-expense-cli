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

	var candidates []reportStatusCandidate
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			if objectReportNumber(typed) == reportNumber {
				candidates = append(candidates, objectReportStatuses(typed)...)
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
	return resolveReportStatusCandidates(candidates)
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

type reportStatusCandidate struct {
	value  string
	score  int
	stable bool
}

func resolveReportStatusCandidates(candidates []reportStatusCandidate) (string, bool, error) {
	var stable, display reportStatusCandidate
	var stableStatus, displayStatus string
	stableFound, displayFound := false, false

	for _, candidate := range candidates {
		normalized := normalizeReportStatus(candidate.value)
		if candidate.stable {
			if stableFound && normalized != stableStatus {
				return "", false, errors.New("expense: submit response contains conflicting statuses for the created report")
			}
			if !stableFound || preferStableStatusCandidate(stable, candidate) {
				stable = candidate
			}
			stableStatus, stableFound = normalized, true
			continue
		}

		if displayFound && normalized != displayStatus {
			// Defer display conflicts until stable code evidence is known. Enum
			// labels can be localized, while ApprovalStatus_field codes are stable.
			continue
		}
		if !displayFound || candidate.score > display.score {
			display = candidate
		}
		displayStatus, displayFound = normalized, true
	}

	if stableFound {
		for _, candidate := range candidates {
			if candidate.stable {
				continue
			}
			normalized := normalizeReportStatus(candidate.value)
			if isModeledReportStatus(normalized) && normalized != stableStatus {
				return "", false, errors.New("expense: submit response contains conflicting statuses for the created report")
			}
		}
		return stable.value, true, nil
	}

	if !displayFound {
		return "", false, nil
	}
	for _, candidate := range candidates {
		if normalizeReportStatus(candidate.value) != displayStatus {
			return "", false, errors.New("expense: submit response contains conflicting statuses for the created report")
		}
	}
	switch displayStatus {
	case "draft":
		return "Draft", true, nil
	case "submitted":
		return "Submitted", true, nil
	default:
		return display.value, true, nil
	}
}

func preferStableStatusCandidate(current, candidate reportStatusCandidate) bool {
	candidateCode := strings.TrimSpace(candidate.value) == "1" || strings.TrimSpace(candidate.value) == "2"
	currentCode := strings.TrimSpace(current.value) == "1" || strings.TrimSpace(current.value) == "2"
	return candidateCode && !currentCode || candidateCode == currentCode && candidate.score > current.score
}

func objectReportStatuses(object map[string]any) []reportStatusCandidate {
	var candidates []reportStatusCandidate
	for name, value := range object {
		text, ok := submissionScalar(value)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		normalizedName := normalizeSubmissionProperty(name)
		score := submissionStatusPropertyScore(normalizedName)
		if score > 0 {
			candidates = append(candidates, reportStatusCandidate{
				value:  text,
				score:  score,
				stable: normalizedName == "approvalstatusfield",
			})
		}
	}
	return candidates
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
	return normalizeReportStatus(status) == "draft"
}

func isSubmittedStatus(status string) bool {
	return normalizeReportStatus(status) == "submitted"
}

func isModeledReportStatus(status string) bool {
	return status == "draft" || status == "submitted"
}

func normalizeReportStatus(status string) string {
	normalized := strings.ToLower(strings.TrimSpace(status))
	switch normalized {
	case "1", "draft":
		return "draft"
	case "2", "submitted":
		return "submitted"
	default:
		return normalized
	}
}
