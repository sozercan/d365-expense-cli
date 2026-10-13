package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/expense"
)

func writeExpenseFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "expenses.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadExpenseFileKeepsExactAmountText(t *testing.T) {
	t.Parallel()
	path := writeExpenseFile(t, `[
		{"category": "Hotel", "date": "2026-10-10", "amount": "300.00", "merchant": "Hotel", "notes": "Trip",
		 "itemization": [{"subcategory": "Daily Room Rate", "start_date": "2026-10-10", "daily_rate": 125.5, "days": 2},
		                 {"subcategory": "Hotel Tax", "daily_rate": "49", "days": 1}]},
		{"category": "Taxi", "date": "2026-10-11", "amount": 42.10, "currency": "EUR"}
	]`)
	got, err := loadExpenseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []expense.ExpenseLineInput{
		{Category: "Hotel", Date: "2026-10-10", Amount: "300.00", Merchant: "Hotel", Notes: "Trip", Itemization: []expense.ItemizationInput{
			{Subcategory: "Daily Room Rate", StartDate: "2026-10-10", DailyRate: "125.5", Days: 2},
			{Subcategory: "Hotel Tax", DailyRate: "49", Days: 1},
		}},
		{Category: "Taxi", Date: "2026-10-11", Amount: "42.10", Currency: "EUR"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadExpenseFile() = %#v, want %#v", got, want)
	}
}

func TestLoadExpenseFileRejectsMalformedInput(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"unknown field": `[{"category": "Hotel", "date": "2026-10-10", "amount": "1", "nights": 2}]`,
		"empty":         `[]`,
		"object":        `{"category": "Hotel"}`,
		"trailing":      `[{"category": "Hotel", "date": "2026-10-10", "amount": "1"}] []`,
		"amount type":   `[{"category": "Hotel", "date": "2026-10-10", "amount": true}]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadExpenseFile(writeExpenseFile(t, content)); err == nil {
				t.Fatal("loadExpenseFile() accepted malformed input")
			}
		})
	}
}

func TestCanonicalCreateExpensesRequireDraftAndExtendTimeout(t *testing.T) {
	t.Parallel()
	path := writeExpenseFile(t, `[{"category": "Hotel", "date": "2026-10-10", "amount": "1"}, {"category": "Taxi", "date": "2026-10-10", "amount": "2"}]`)

	var stdout, stderr bytes.Buffer
	if code := runCanonical([]string{"create", "--session", "work", "--purpose", "event", "--expenses", path, "--dry-run"}, &stdout, &stderr); code != 2 {
		t.Fatalf("submit with expenses code = %d", code)
	}
	if !strings.Contains(stderr.String(), "--expenses currently requires --draft") {
		t.Fatalf("stderr = %q", stderr.String())
	}

	var got []string
	runners := defaultLegacyRunners()
	runners.createReport = func(args []string, _, _ io.Writer) int {
		got = append([]string(nil), args...)
		return 0
	}
	stdout.Reset()
	stderr.Reset()
	if code := runCanonicalWithRunners([]string{"create", "--draft", "--session", "work", "--purpose", "event", "--expenses", path}, &stdout, &stderr, runners); code != 0 {
		t.Fatalf("code = %d, stderr=%q", code, stderr.String())
	}
	want := []string{"--session", "work", "--purpose", "event", "--expenses", path, "--timeout", "3m45s", "--execute"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}
