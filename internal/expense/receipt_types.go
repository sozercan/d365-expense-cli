package expense

import "io"

// ReceiptInput describes one bounded receipt without exposing filesystem paths
// to the receipt workflow. Open is called only during explicit execution.
type ReceiptInput struct {
	Filename  string
	MediaType string
	Size      int64
	Open      func() (io.ReadCloser, error)
}

// AttachReceiptRequest attaches one receipt to the report captured by a
// receipt-capable HAR session.
type AttachReceiptRequest struct {
	ReportNumber string
	Notes        string
	Receipt      ReceiptInput
}

// ReceiptSummary is safe to print during a dry run.
type ReceiptSummary struct {
	Filename  string
	MediaType string
	Size      int64
}

// AttachReceiptPlan describes the mutation without opening the local file or
// making network requests.
type AttachReceiptPlan struct {
	ReportNumber string
	Receipt      ReceiptSummary
	RequestCount int
	Actions      []string
}

// AttachedReceipt is the safe result of a successful attachment.
type AttachedReceipt struct {
	Filename string
	Size     int64
}

// AttachReceiptResult reports only non-secret outcome data.
type AttachReceiptResult struct {
	ReportNumber   string
	Status         string
	ReceiptCount   int
	Attached       AttachedReceipt
	SavedAndClosed bool
}
