package dynamics

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// ReceiptModel is the receipt-specific subset discovered from one or more
// Dynamics responses. AccessToken is intentionally omitted by String and
// SafeSummary; callers should treat it as a credential.
type ReceiptModel struct {
	AddNewReceiptForm    ModelNode
	UploadControl        ModelNode
	OKButton             ModelNode
	CloseButton          ModelNode
	AddReceiptButton     ModelNode
	ReceiptsTabPage      ModelNode
	SaveAndClose         ModelNode
	SubmitButton         ModelNode
	AccessToken          string
	CurrentRecID         string
	CurrentDocuRefRecID  string
	CurrentTableID       string
	SelectedDocumentType string
	ReportNumber         string
	Status               string
	ReceiptCount         int
	ReceiptCountPresent  bool
}

// DiscoverReceiptModel extracts receipt form/control IDs and upload metadata
// without depending on concrete server interaction types.
func DiscoverReceiptModel(data []byte) (ReceiptModel, error) {
	response, err := DiscoverResponseModel(data)
	if err != nil {
		return ReceiptModel{}, fmt.Errorf("discover receipt model: %w", err)
	}
	return receiptModelFromResponse(response), nil
}

// DiscoverReceiptEnvelopeModel discovers receipt data from a parsed envelope.
func DiscoverReceiptEnvelopeModel(envelope Envelope) (ReceiptModel, error) {
	data, err := MarshalEnvelope(envelope)
	if err != nil {
		return ReceiptModel{}, errorsWithoutCredential("marshal receipt envelope", err)
	}
	return DiscoverReceiptModel(data)
}

func receiptModelFromResponse(response ResponseModel) ReceiptModel {
	model := ReceiptModel{}
	detailsForm, hasDetailsForm := response.FindForm(FormExpenseReportDetails)
	model.AddNewReceiptForm, _ = response.FindForm(FormExpenseAddNewReceipt)
	model.UploadControl, _ = response.FindControl(ControlUploadControl)
	model.OKButton = firstControl(response, ControlOKButtonAddNewTabPage, "OKButton", "OkButton", "OK")
	model.CloseButton = firstControl(response, ControlCloseButtonAddNewTabPage)
	model.AddReceiptButton = firstControl(response, ControlNewReceiptButton, ControlAddReceipts)
	model.ReceiptsTabPage, _ = response.FindControl(ControlReceiptsTabPage)
	model.SaveAndClose, _ = response.FindControl(ControlSaveAndClose)
	model.SubmitButton, _ = response.FindControl(ControlSubmitButton)

	model.AccessToken = nodeScalar(model.UploadControl, "AccessToken")
	model.CurrentRecID = nodeScalar(model.UploadControl, "CurrentRecId")
	model.CurrentDocuRefRecID = nodeScalar(model.UploadControl, "CurrentDocuRefRecId")
	model.CurrentTableID = nodeScalar(model.UploadControl, "CurrentTableId")
	model.SelectedDocumentType = nodeScalar(model.UploadControl, "SelectedDocumentType")

	dialogReport := reportNumberFromTitleFields(rawScalar(model.AddNewReceiptForm.ValueProperties["ParentTitleFields"]))
	switch {
	case dialogReport != "":
		// The receipt dialog's ParentTitleFields is direct, form-local report
		// identity and must win over unrelated workspace/detail updates that can
		// be serialized in the same stateful response.
		model.ReportNumber = dialogReport
	case hasDetailsForm && detailsForm.ID != "":
		// Generic report properties are meaningful to the receipt flow only when
		// this response actually carries an ExpenseReportDetails_form. Stage-local
		// CheckFile/upload responses can contain stale properties for a previously
		// closed report; treating those as current identity causes false mismatches.
		model.ReportNumber = response.ReportNumber
	}
	receiptCount, hasReceiptCount := response.FindControl(ControlReceiptCount)
	if hasReceiptCount {
		if value, present := rawInteger(receiptCount.ValueProperties["Value"]); present && value >= 0 {
			model.ReceiptCount = value
			model.ReceiptCountPresent = true
		}
	}
	if hasDetailsForm || model.AddReceiptButton.RootID != "" || model.ReceiptsTabPage.RootID != "" ||
		model.SaveAndClose.RootID != "" || model.SubmitButton.RootID != "" || (hasReceiptCount && receiptCount.RootID != "") {
		// Status is report-local only when the response carries the details form
		// or one of its receipt controls. Stage-local upload responses can include
		// stale status properties from a previously closed report.
		model.Status = response.Status
	}
	return model
}

func firstControl(response ResponseModel, names ...string) ModelNode {
	for _, name := range names {
		if node, ok := response.FindControl(name); ok {
			return node
		}
	}
	return ModelNode{}
}

func nodeScalar(node ModelNode, name string) string {
	for _, properties := range []map[string]json.RawMessage{node.ValueProperties, node.SerializedValueProperties, node.Properties} {
		if value := rawScalar(properties[name]); value != "" {
			return value
		}
	}
	return ""
}

func rawScalar(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String()
	}
	return ""
}

func rawInteger(raw json.RawMessage) (int, bool) {
	text := rawScalar(raw)
	if text == "" {
		return 0, false
	}
	value, err := strconv.Atoi(text)
	return value, err == nil
}

// MergeReceiptModels combines stage-local discoveries, preferring later
// non-empty values. It is useful because the details, tab, and upload-dialog
// controls arrive in separate captured responses.
func MergeReceiptModels(models ...ReceiptModel) ReceiptModel {
	var merged ReceiptModel
	for _, model := range models {
		mergeNode := func(destination *ModelNode, source ModelNode) {
			if source.ID != "" {
				*destination = source
			}
		}
		mergeNode(&merged.AddNewReceiptForm, model.AddNewReceiptForm)
		mergeNode(&merged.UploadControl, model.UploadControl)
		mergeNode(&merged.OKButton, model.OKButton)
		mergeNode(&merged.CloseButton, model.CloseButton)
		mergeNode(&merged.AddReceiptButton, model.AddReceiptButton)
		mergeNode(&merged.ReceiptsTabPage, model.ReceiptsTabPage)
		mergeNode(&merged.SaveAndClose, model.SaveAndClose)
		mergeNode(&merged.SubmitButton, model.SubmitButton)
		for destination, source := range map[*string]string{
			&merged.AccessToken: model.AccessToken, &merged.CurrentRecID: model.CurrentRecID,
			&merged.CurrentDocuRefRecID: model.CurrentDocuRefRecID, &merged.CurrentTableID: model.CurrentTableID,
			&merged.SelectedDocumentType: model.SelectedDocumentType, &merged.ReportNumber: model.ReportNumber,
			&merged.Status: model.Status,
		} {
			if source != "" {
				*destination = source
			}
		}
		if model.ReceiptCountPresent {
			merged.ReceiptCount = model.ReceiptCount
			merged.ReceiptCountPresent = true
		}
	}
	return merged
}

// CommandTargets returns the allowlist IDs represented by this discovery.
func (model ReceiptModel) CommandTargets() ReceiptCommandTargets {
	detailsRootID := firstNonEmpty(model.AddReceiptButton.RootID, model.ReceiptsTabPage.RootID, model.SaveAndClose.RootID, model.SubmitButton.RootID)
	dialogRootID := firstNonEmpty(model.AddNewReceiptForm.ID, model.UploadControl.RootID, model.OKButton.RootID)
	return ReceiptCommandTargets{
		DetailsRootID:      detailsRootID,
		NewReceiptButtonID: model.AddReceiptButton.ID,
		DialogRootID:       dialogRootID,
		UploadControlID:    model.UploadControl.ID,
		OKButtonID:         model.OKButton.ID,
		CloseButtonID:      model.CloseButton.ID,
		SaveAndCloseID:     model.SaveAndClose.ID,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// SafeSummary reports discovery completeness without including AccessToken.
func (model ReceiptModel) SafeSummary() string {
	return fmt.Sprintf(
		"receipt model: report=%q status=%q receipt-count=%d receipt-count-present=%t form=%t upload=%t ok=%t close=%t add=%t tab=%t save=%t submit=%t access-token-present=%t",
		model.ReportNumber, model.Status, model.ReceiptCount, model.ReceiptCountPresent,
		model.AddNewReceiptForm.ID != "", model.UploadControl.ID != "", model.OKButton.ID != "", model.CloseButton.ID != "",
		model.AddReceiptButton.ID != "", model.ReceiptsTabPage.ID != "", model.SaveAndClose.ID != "", model.SubmitButton.ID != "",
		model.AccessToken != "",
	)
}

func (model ReceiptModel) String() string   { return model.SafeSummary() }
func (model ReceiptModel) GoString() string { return model.SafeSummary() }

func errorsWithoutCredential(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %s", operation, err.Error())
}
