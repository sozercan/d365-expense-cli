package expense

import (
	"errors"
	"slices"

	"github.com/sozercan/d365-expense-cli/internal/capture"
)

const (
	receiptUploadEndpointPath = "/filemanagement"
	receiptDocumentType       = "File"
)

var receiptMultipartFieldOrder = []string{
	"clientId",
	"maxChunkSize",
	"tableid",
	"recid",
	"companyid",
	"accesstoken",
	"notes",
	"docuname",
	"docutypeid",
	"ischunked",
	"docuRefRecId",
	"files[]",
}

// ReceiptUploadContract is the validated, non-secret subset of a captured
// receipt protocol profile needed to upload one PNG. Its fields are private so
// callers must use the built-in contract or derive one from a validated capture
// rather than inventing an upload shape.
type ReceiptUploadContract struct {
	endpointPath               string
	multipartFieldOrder        []string
	maxChunkSize               int64
	documentType               string
	maxSupportedSingleFileSize int64
}

// BuiltinReceiptUploadContract returns the validated, non-secret receipt
// upload contract supported by this client. It removes the need to retain a
// receipt HAR solely for these fixed protocol constants.
func BuiltinReceiptUploadContract() ReceiptUploadContract {
	contract, err := newReceiptUploadContract(capture.ReceiptUploadProfile{
		EndpointPath:               receiptUploadEndpointPath,
		MultipartFieldOrder:        receiptMultipartFieldOrder,
		MaxChunkSize:               maxReceiptFileSize,
		DocumentType:               receiptDocumentType,
		MaxSupportedSingleFileSize: maxReceiptFileSize,
	})
	if err != nil {
		panic("expense: invalid built-in receipt upload contract: " + err.Error())
	}
	return contract
}

// DefaultReceiptUploadContract is an alias for BuiltinReceiptUploadContract.
func DefaultReceiptUploadContract() ReceiptUploadContract {
	return BuiltinReceiptUploadContract()
}

// MaxSupportedSingleFileSize returns the maximum receipt size accepted by this
// upload contract. A zero-value or otherwise invalid contract returns zero.
func (contract ReceiptUploadContract) MaxSupportedSingleFileSize() int64 {
	if contract.validate() != nil {
		return 0
	}
	return contract.maxSupportedSingleFileSize
}

// ReceiptUploadContractFromProfile validates and snapshots only the non-secret
// upload contract from a receipt profile. It deliberately ignores the profile's
// report number, control IDs, session credentials, and sequence state.
func ReceiptUploadContractFromProfile(profile *capture.ReceiptProfile) (ReceiptUploadContract, error) {
	if profile == nil {
		return ReceiptUploadContract{}, errors.New("expense: receipt protocol profile is nil")
	}
	return newReceiptUploadContract(profile.Upload)
}

func newReceiptUploadContract(profile capture.ReceiptUploadProfile) (ReceiptUploadContract, error) {
	if profile.EndpointPath != receiptUploadEndpointPath ||
		!slices.Equal(profile.MultipartFieldOrder, receiptMultipartFieldOrder) ||
		profile.MaxChunkSize != maxReceiptFileSize ||
		profile.MaxSupportedSingleFileSize != maxReceiptFileSize ||
		profile.DocumentType != receiptDocumentType {
		return ReceiptUploadContract{}, errors.New("expense: receipt upload contract is unsupported")
	}
	return ReceiptUploadContract{
		endpointPath:               profile.EndpointPath,
		multipartFieldOrder:        append([]string(nil), profile.MultipartFieldOrder...),
		maxChunkSize:               profile.MaxChunkSize,
		documentType:               profile.DocumentType,
		maxSupportedSingleFileSize: profile.MaxSupportedSingleFileSize,
	}, nil
}

func (contract ReceiptUploadContract) validate() error {
	_, err := newReceiptUploadContract(capture.ReceiptUploadProfile{
		EndpointPath:               contract.endpointPath,
		MultipartFieldOrder:        contract.multipartFieldOrder,
		MaxChunkSize:               contract.maxChunkSize,
		DocumentType:               contract.documentType,
		MaxSupportedSingleFileSize: contract.maxSupportedSingleFileSize,
	})
	return err
}

// CreateReportReceiptInput pairs one receipt with the notes sent for that
// receipt. Inputs are attached in slice order by CreateReportWithReceipts.
type CreateReportReceiptInput struct {
	Notes   string
	Receipt ReceiptInput
}

// CreateReportReceiptSummary is safe to print during a multi-receipt dry run.
// It deliberately omits arbitrary receipt notes.
type CreateReportReceiptSummary struct {
	Receipt ReceiptSummary
}

// CreateReportReceiptResult records one successful attachment and the
// cumulative report receipt count immediately after it was confirmed.
type CreateReportReceiptResult struct {
	Attached          AttachedReceipt
	ReceiptCountAfter int
}

// CreateReportWithReceiptsRequest describes one report-creation workflow with
// one or more PNG receipts and an explicit final action.
type CreateReportWithReceiptsRequest struct {
	Purpose        string
	Receipts       []CreateReportReceiptInput
	UploadContract ReceiptUploadContract
	FinalAction    ReportFinalAction
}

// CreateReportWithReceiptsPlan is a credential-free, offline description of a
// report creation and ordered multi-receipt mutation.
type CreateReportWithReceiptsPlan struct {
	Purpose      string
	Receipts     []CreateReportReceiptSummary
	RequestCount int
	Actions      []string
}

// CreateReportWithReceiptsResult contains only non-secret outcome data from the
// combined workflow. Receipts preserves the request order.
type CreateReportWithReceiptsResult struct {
	Purpose            string
	ReportNumber       string
	Status             string
	ReceiptCountBefore int
	ReceiptCountAfter  int
	Receipts           []CreateReportReceiptResult
	SavedAndClosed     bool
	Submitted          bool
}
