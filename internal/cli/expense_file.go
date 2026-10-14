package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sozercan/d365-expense-cli/internal/expense"
)

const maxExpenseFileSize = 1 << 20

type expenseFileLine struct {
	Category    string            `json:"category"`
	Date        string            `json:"date"`
	Amount      decimalText       `json:"amount"`
	Currency    string            `json:"currency"`
	Merchant    string            `json:"merchant"`
	Notes       string            `json:"notes"`
	Itemization []expenseFileItem `json:"itemization"`
}

type expenseFileItem struct {
	Subcategory string      `json:"subcategory"`
	StartDate   string      `json:"start_date"`
	DailyRate   decimalText `json:"daily_rate"`
	Days        int         `json:"days"`
}

// decimalText accepts a JSON string or number and keeps its exact text so
// amounts are never rounded through float64.
type decimalText string

func (text *decimalText) UnmarshalJSON(data []byte) error {
	if bytes.HasPrefix(data, []byte(`"`)) {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*text = decimalText(value)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return errors.New("amount must be a JSON string or number")
	}
	*text = decimalText(number.String())
	return nil
}

// loadItemizationFile reads a JSON array of itemization rows.
func loadItemizationFile(path string) ([]expense.ItemizationInput, error) {
	data, err := readExpenseJSON(path)
	if err != nil {
		return nil, err
	}
	var items []expenseFileItem
	if err := decodeStrictJSON(data, &items); err != nil {
		return nil, fmt.Errorf("parse itemization file: %w", err)
	}
	if len(items) == 0 {
		return nil, errors.New("itemization file contains no rows")
	}
	rows := make([]expense.ItemizationInput, len(items))
	for index, item := range items {
		rows[index] = expense.ItemizationInput{Subcategory: item.Subcategory, StartDate: item.StartDate, DailyRate: string(item.DailyRate), Days: item.Days}
	}
	return rows, nil
}

func readExpenseJSON(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxExpenseFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxExpenseFileSize {
		return nil, fmt.Errorf("%s exceeds 1 MiB", path)
	}
	return data, nil
}

// decodeStrictJSON decodes exactly one JSON value with no unknown fields.
func decodeStrictJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

// loadExpenseFile reads a JSON array of expense lines. Semantic validation is
// left to the expense package so dry runs and execution share one check.
func loadExpenseFile(path string) ([]expense.ExpenseLineInput, error) {
	data, err := readExpenseJSON(path)
	if err != nil {
		return nil, err
	}
	var lines []expenseFileLine
	if err := decodeStrictJSON(data, &lines); err != nil {
		return nil, fmt.Errorf("parse expenses file: %w", err)
	}
	if len(lines) == 0 {
		return nil, errors.New("expenses file contains no expense lines")
	}
	inputs := make([]expense.ExpenseLineInput, len(lines))
	for index, line := range lines {
		inputs[index] = expense.ExpenseLineInput{
			Category: line.Category,
			Date:     line.Date,
			Amount:   string(line.Amount),
			Currency: line.Currency,
			Merchant: line.Merchant,
			Notes:    line.Notes,
		}
		for _, item := range line.Itemization {
			inputs[index].Itemization = append(inputs[index].Itemization, expense.ItemizationInput{
				Subcategory: item.Subcategory,
				StartDate:   item.StartDate,
				DailyRate:   string(item.DailyRate),
				Days:        item.Days,
			})
		}
	}
	return inputs, nil
}

func printExpensePlan(w io.Writer, lines []expense.ExpenseLineInput) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "expenses: %d\n", len(lines))
	for index, line := range lines {
		fmt.Fprintf(w, "- expense %d: %s %s %s", index+1, line.Date, line.Category, line.Amount)
		if line.Currency != "" {
			fmt.Fprintf(w, " %s", line.Currency)
		}
		if len(line.Itemization) != 0 {
			fmt.Fprintf(w, ", itemized into %d rows", len(line.Itemization))
		}
		fmt.Fprintln(w)
	}
}

func printExpenseResults(w io.Writer, results []expense.ExpenseLineResult) {
	for index, result := range results {
		fmt.Fprintf(w, "- expense %d: %s %s %s added", index+1, result.Date, result.Category, result.Amount)
		if result.ItemizedLines != 0 {
			fmt.Fprintf(w, ", itemized-lines=%d", result.ItemizedLines)
		}
		fmt.Fprintln(w)
	}
}
