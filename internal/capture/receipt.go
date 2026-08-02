package capture

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
)

const (
	receiptDetailsFormName        = "ExpenseReportDetails_form"
	receiptDialogFormName         = "ExpenseAddNewReceipt_form"
	receiptAddControlName         = "NewReceiptButton"
	receiptUploadControlName      = "UploadControl"
	receiptOKControlName          = "OkButtonAddNewTabPage"
	receiptCountControlName       = "ReceiptCount"
	receiptSaveControlName        = "SaveAndClose"
	receiptUploadEndpointPath     = "/filemanagement"
	receiptUploadedFileIDProperty = "UploadedFileId"
	receiptDocumentType           = "File"
	receiptSequenceHeadroom       = int64(8)
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

// ReceiptProfile is the validated result of parsing a receipt-attachment HAR.
// It contains only the current replayable form targets and non-secret upload
// protocol metadata. Captured upload tokens, client IDs, file IDs, boundaries,
// and receipt bytes are deliberately not retained.
type ReceiptProfile struct {
	Session           SessionProfile
	ReportNumber      string
	ReportStatus      string
	ReceiptCount      int
	DetailsFormRootID string
	AddReceipts       CommandTarget
	SaveAndClose      CommandTarget
	Expected          ReceiptExpectedNames
	Upload            ReceiptUploadProfile
}

// ReceiptExpectedNames identifies the forms and controls a receipt client must
// require in response models before using their dynamic IDs.
type ReceiptExpectedNames struct {
	DetailsForm         string
	AddReceiptForm      string
	AddReceiptsControl  string
	UploadControl       string
	OKControl           string
	ReceiptCountControl string
	SaveAndCloseControl string
}

// ReceiptUploadProfile contains the observed, non-secret multipart contract.
type ReceiptUploadProfile struct {
	EndpointPath               string
	MultipartFieldOrder        []string
	MaxChunkSize               int64
	DocumentType               string
	MaxSupportedSingleFileSize int64
}

// LoadReceipt opens and parses a receipt-attachment HAR 1.2 file.
func LoadReceipt(path string) (*ReceiptProfile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open receipt HAR: %w", err)
	}
	defer f.Close()

	profile, err := ParseReceipt(f)
	if err != nil {
		return nil, fmt.Errorf("parse receipt HAR: %w", err)
	}
	return profile, nil
}

// ParseReceipt reads a HAR 1.2 document and returns a complete, validated
// receipt-attachment profile. Parse and Load retain their existing draft-only
// behavior.
func ParseReceipt(r io.Reader) (*ReceiptProfile, error) {
	if r == nil {
		return nil, errors.New("receipt HAR reader is nil")
	}

	decoder := json.NewDecoder(r)
	var document harDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode receipt HAR: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("decode receipt HAR: trailing JSON value")
		}
		return nil, fmt.Errorf("decode receipt HAR trailing data: %w", err)
	}
	if document.Log.Version != "1.2" {
		return nil, errors.New("unsupported receipt HAR version; want 1.2")
	}

	exchanges, err := collectExchanges(document.Log.Entries)
	if err != nil {
		return nil, err
	}
	if len(exchanges) == 0 {
		return nil, errors.New("receipt HAR contains no Dynamics ProcessMessages requests")
	}
	selected, err := selectExchangeGroup(exchanges)
	if err != nil {
		return nil, err
	}

	session, err := extractReceiptSession(selected)
	if err != nil {
		return nil, err
	}
	for i := range selected {
		if selected[i].responseParseErr != nil {
			return nil, selected[i].responseParseErr
		}
	}

	profile, err := extractReceiptProfile(document.Log.Entries, selected, session)
	if err != nil {
		return nil, err
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	return profile, nil
}

// Validate verifies that a receipt profile is complete, replayable, and still
// describes only the allowlisted Draft receipt flow.
func (p *ReceiptProfile) Validate() error {
	if p == nil {
		return errors.New("receipt capture profile is nil")
	}
	if err := validateEndpoint(p.Session.BaseURL, p.Session.EndpointURL); err != nil {
		return err
	}
	if strings.TrimSpace(p.Session.Company) == "" || strings.TrimSpace(p.Session.Language) == "" {
		return errors.New("receipt capture session company or language is missing")
	}
	if p.Session.ChannelID < 0 || p.Session.LastServerSequence < 0 || p.Session.NextClientSequence <= 0 {
		return errors.New("receipt capture sequence state is invalid")
	}
	if p.Session.NextClientSequence > math.MaxInt64-receiptSequenceHeadroom {
		return errors.New("receipt capture client sequence lacks headroom")
	}
	if err := validateCredentials(p.Session.RequestHeaders, p.Session.Cookies); err != nil {
		return err
	}
	if strings.TrimSpace(p.ReportNumber) == "" {
		return errors.New("receipt capture report number is missing")
	}
	if p.ReportStatus != "Draft" {
		return errors.New("receipt capture report status is not exactly Draft")
	}
	if p.ReceiptCount < 0 {
		return errors.New("receipt capture receipt count is invalid")
	}
	if strings.TrimSpace(p.DetailsFormRootID) == "" {
		return errors.New("receipt capture current details-form root is missing")
	}
	if err := validateTarget("AddReceipts", p.AddReceipts); err != nil {
		return err
	}
	if err := validateTarget("SaveAndClose", p.SaveAndClose); err != nil {
		return err
	}
	if p.AddReceipts.CommandName != "Click" || p.AddReceipts.RootID != p.DetailsFormRootID || p.AddReceipts.ControlName != receiptAddControlName {
		return errors.New("receipt capture AddReceipts target is not allowlisted")
	}
	if p.SaveAndClose.CommandName != "Click" || p.SaveAndClose.RootID != p.DetailsFormRootID || p.SaveAndClose.ControlName != receiptSaveControlName {
		return errors.New("receipt capture SaveAndClose target is not allowlisted")
	}
	wantNames := defaultReceiptExpectedNames()
	if p.Expected != wantNames {
		return errors.New("receipt capture expected form/control names are invalid")
	}
	if p.Upload.EndpointPath != receiptUploadEndpointPath ||
		!slices.Equal(p.Upload.MultipartFieldOrder, receiptMultipartFieldOrder) ||
		p.Upload.MaxChunkSize <= 0 ||
		p.Upload.MaxSupportedSingleFileSize != p.Upload.MaxChunkSize ||
		p.Upload.DocumentType != receiptDocumentType {
		return errors.New("receipt capture upload contract is invalid")
	}
	return nil
}

// SafeSummary returns a log-friendly receipt profile description. Header,
// cookie, and multipart field names are shown, but no captured values are.
func (p *ReceiptProfile) SafeSummary() string {
	if p == nil {
		return "receipt capture profile: <nil>"
	}

	headerNames := make([]string, 0, len(p.Session.RequestHeaders))
	for name := range p.Session.RequestHeaders {
		headerNames = append(headerNames, name)
	}
	slices.Sort(headerNames)
	cookieNames := make([]string, 0, len(p.Session.Cookies))
	for _, cookie := range p.Session.Cookies {
		if cookie != nil && cookie.Name != "" {
			cookieNames = append(cookieNames, cookie.Name)
		}
	}
	slices.Sort(cookieNames)

	return fmt.Sprintf(
		"receipt capture profile: base=%s endpoint=%s company=%s language=%s channel=%d server-sequence=%d next-client-sequence=%d headers=[%s] cookies=[%s] report=%s status=%s receipt-count=%d details-root=%s add-receipts=%s/%s(%s) save-and-close=%s/%s(%s) expected={details:%s dialog:%s upload:%s ok:%s count:%s} upload={path:%s fields:[%s] max-chunk:%d document-type:%s max-single-file:%d}",
		safeURL(p.Session.BaseURL),
		safeURL(p.Session.EndpointURL),
		p.Session.Company,
		p.Session.Language,
		p.Session.ChannelID,
		p.Session.LastServerSequence,
		p.Session.NextClientSequence,
		strings.Join(headerNames, ","),
		strings.Join(cookieNames, ","),
		p.ReportNumber,
		p.ReportStatus,
		p.ReceiptCount,
		p.DetailsFormRootID,
		p.AddReceipts.RootID,
		p.AddReceipts.TargetID,
		p.AddReceipts.ControlName,
		p.SaveAndClose.RootID,
		p.SaveAndClose.TargetID,
		p.SaveAndClose.ControlName,
		p.Expected.DetailsForm,
		p.Expected.AddReceiptForm,
		p.Expected.UploadControl,
		p.Expected.OKControl,
		p.Expected.ReceiptCountControl,
		p.Upload.EndpointPath,
		strings.Join(p.Upload.MultipartFieldOrder, ","),
		p.Upload.MaxChunkSize,
		p.Upload.DocumentType,
		p.Upload.MaxSupportedSingleFileSize,
	)
}

func defaultReceiptExpectedNames() ReceiptExpectedNames {
	return ReceiptExpectedNames{
		DetailsForm:         receiptDetailsFormName,
		AddReceiptForm:      receiptDialogFormName,
		AddReceiptsControl:  receiptAddControlName,
		UploadControl:       receiptUploadControlName,
		OKControl:           receiptOKControlName,
		ReceiptCountControl: receiptCountControlName,
		SaveAndCloseControl: receiptSaveControlName,
	}
}

func extractReceiptSession(exchanges []exchange) (SessionProfile, error) {
	var latest *exchange
	headers := make(http.Header)
	cookies := make(map[string]*http.Cookie)
	var maxServerSequence int64
	var maxClientSequence int64

	for i := range exchanges {
		ex := &exchanges[i]
		if ex.status < 200 || ex.status >= 300 {
			continue
		}
		latest = ex
		mergeReplayHeaders(headers, ex.headers)
		mergeCookies(cookies, ex.cookies, ex.headers)
		if ack := ex.request.LastAcknowledgedSequenceNumber; ack != nil && *ack > maxServerSequence {
			maxServerSequence = *ack
		}
		for _, message := range ex.request.Messages {
			if message.SequenceNumber != nil && *message.SequenceNumber > maxClientSequence {
				maxClientSequence = *message.SequenceNumber
			}
		}
		if ex.response != nil {
			if ack := ex.response.LastAcknowledgedSequenceNumber; ack != nil && *ack > maxClientSequence {
				maxClientSequence = *ack
			}
			for _, message := range ex.response.Messages {
				if message.SequenceNumber != nil && *message.SequenceNumber > maxServerSequence {
					maxServerSequence = *message.SequenceNumber
				}
			}
		}
	}
	if latest == nil {
		return SessionProfile{}, errors.New("receipt HAR contains no successful Dynamics ProcessMessages requests")
	}
	if maxClientSequence > math.MaxInt64-receiptSequenceHeadroom-1 {
		return SessionProfile{}, errors.New("captured client sequence lacks headroom for receipt attachment")
	}
	baseURL, err := baseURLForEndpoint(latest.endpoint)
	if err != nil {
		return SessionProfile{}, err
	}
	session := SessionProfile{
		BaseURL:            baseURL,
		EndpointURL:        latest.endpoint.String(),
		RequestHeaders:     headers,
		Cookies:            sortedCookies(cookies),
		Company:            latest.request.CompanyID,
		Language:           latest.request.Language,
		ChannelID:          *latest.request.ChannelID,
		LastServerSequence: maxServerSequence,
		NextClientSequence: maxClientSequence + 1,
	}
	if err := validateCredentials(session.RequestHeaders, session.Cookies); err != nil {
		return SessionProfile{}, err
	}
	return session, nil
}

type receiptCommandKey struct {
	rootID   string
	targetID string
}

type receiptUploadObservation struct {
	entryIndex   int
	fieldOrder   []string
	maxChunkSize int64
	documentType string
	fileID       string
}

type receiptFormState struct {
	activeDetails map[string]int
	activeDialogs map[string]int
	parentReports map[string]map[string]bool
	statuses      map[string][]string
}

func extractReceiptProfile(entries []harEntry, exchanges []exchange, session SessionProfile) (*ReceiptProfile, error) {
	catalog := newTargetCatalog()
	for i := range exchanges {
		if exchanges[i].response != nil {
			catalog.collect(exchanges[i].responseBody)
		}
	}
	if receiptHasForbiddenCommand(exchanges, catalog) {
		return nil, errors.New("receipt HAR contains a forbidden expense workflow command")
	}

	state, err := collectReceiptFormState(exchanges)
	if err != nil {
		return nil, err
	}
	currentRoot, ok := onlyStringKey(state.activeDetails)
	if !ok {
		return nil, errors.New("receipt HAR does not leave one unambiguous current ExpenseReportDetails_form")
	}
	if len(state.activeDialogs) != 0 {
		return nil, errors.New("receipt HAR leaves a receipt dialog open instead of the current details form")
	}
	if err := requireExactDraftStatus(state.statuses[currentRoot]); err != nil {
		return nil, err
	}

	addTargetID, ok := uniqueCatalogTarget(catalog, currentRoot, receiptAddControlName)
	if !ok {
		return nil, errors.New("receipt HAR current details form lacks one NewReceiptButton")
	}
	saveTargetID, ok := uniqueCatalogTarget(catalog, currentRoot, receiptSaveControlName)
	if !ok {
		return nil, errors.New("receipt HAR current details form lacks one SaveAndClose control")
	}
	if _, ok := uniqueCatalogTarget(catalog, currentRoot, receiptCountControlName); !ok {
		return nil, errors.New("receipt HAR current details form lacks one ReceiptCount control")
	}

	addClicks := make([]int, 0, 1)
	for i := range exchanges {
		ex := &exchanges[i]
		if !exchangeSucceeded(ex) {
			continue
		}
		for _, command := range ex.commands {
			if command.CommandName == "Click" && command.RootID == currentRoot && command.TargetID == addTargetID {
				if !responseHasExpectedReceiptDialog(ex.responseBody) {
					continue
				}
				addClicks = append(addClicks, i)
			}
		}
	}
	if len(addClicks) != 1 {
		return nil, errors.New("receipt HAR lacks one unambiguous Add receipts dialog flow")
	}

	uploadKey, checkLastEntry, err := findReceiptCheckFileFlow(exchanges, catalog)
	if err != nil {
		return nil, err
	}
	upload, err := findReceiptUpload(entries, exchanges, session, checkLastEntry)
	if err != nil {
		return nil, err
	}
	closeExchange, err := findReceiptCloseFlow(exchanges, uploadKey, upload)
	if err != nil {
		return nil, err
	}
	okExchange, attachedRoot, afterCount, err := findReceiptOKFlow(exchanges, catalog, uploadKey.rootID, closeExchange)
	if err != nil {
		return nil, err
	}
	if err := requireExactDraftStatus(statusesForRootBeforeOrAt(exchanges, attachedRoot, okExchange)); err != nil {
		return nil, err
	}
	if !receiptCountIncreased(exchanges, attachedRoot, okExchange, afterCount) {
		return nil, errors.New("receipt HAR lacks a receipt-count increase after OK")
	}
	saveExchange, err := findReceiptSaveFlow(exchanges, catalog, attachedRoot, okExchange)
	if err != nil {
		return nil, err
	}

	reportNumber, ok := onlyStringKey(state.parentReports[currentRoot])
	if !ok || strings.TrimSpace(reportNumber) == "" {
		return nil, errors.New("receipt HAR current details form lacks one captured report number")
	}
	if !responseContainsReportNumber(exchanges[saveExchange].responseBody, reportNumber) {
		return nil, errors.New("receipt HAR SaveAndClose response does not match the current report")
	}

	profile := &ReceiptProfile{
		Session:           session,
		ReportNumber:      reportNumber,
		ReportStatus:      "Draft",
		ReceiptCount:      int(afterCount),
		DetailsFormRootID: currentRoot,
		AddReceipts: CommandTarget{
			CommandName: "Click",
			RootID:      currentRoot,
			TargetID:    addTargetID,
			ControlName: receiptAddControlName,
		},
		SaveAndClose: CommandTarget{
			CommandName: "Click",
			RootID:      currentRoot,
			TargetID:    saveTargetID,
			ControlName: receiptSaveControlName,
		},
		Expected: defaultReceiptExpectedNames(),
		Upload: ReceiptUploadProfile{
			EndpointPath:               receiptUploadEndpointPath,
			MultipartFieldOrder:        slices.Clone(upload.fieldOrder),
			MaxChunkSize:               upload.maxChunkSize,
			DocumentType:               upload.documentType,
			MaxSupportedSingleFileSize: upload.maxChunkSize,
		},
	}
	return profile, nil
}

func collectReceiptFormState(exchanges []exchange) (receiptFormState, error) {
	state := receiptFormState{
		activeDetails: make(map[string]int),
		activeDialogs: make(map[string]int),
		parentReports: make(map[string]map[string]bool),
		statuses:      make(map[string][]string),
	}
	for i := range exchanges {
		ex := &exchanges[i]
		if ex.response == nil {
			continue
		}
		for _, message := range ex.response.Messages {
			for _, raw := range message.Interactions {
				var interaction map[string]any
				if err := json.Unmarshal(raw, &interaction); err != nil {
					return receiptFormState{}, errors.New("decode receipt response interaction")
				}
				typeName, _ := interaction["$type"].(string)
				rootID, _ := interaction["RootId"].(string)
				if strings.HasSuffix(strings.ToLower(typeName), "deleteviewmodelinteraction") {
					targetID, _ := interaction["TargetId"].(string)
					delete(state.activeDetails, targetID)
					delete(state.activeDialogs, targetID)
				}
				descriptor, _ := interaction["Descriptor"].(map[string]any)
				if descriptor == nil {
					continue
				}
				id, _ := descriptor["Id"].(string)
				name, _ := descriptor["Name"].(string)
				if id == "" {
					id = rootID
				}
				switch name {
				case receiptDetailsFormName:
					state.activeDetails[id] = ex.entryIndex
					if report := descriptorParentReport(descriptor); report != "" {
						addStringSet(state.parentReports, id, report)
					}
				case receiptDialogFormName:
					state.activeDialogs[id] = ex.entryIndex
					if report := descriptorParentReport(descriptor); report != "" {
						addStringSet(state.parentReports, id, report)
					}
				}
				for _, value := range recursivePropertyStrings(descriptor, "expenseReportStatus_dataMethod") {
					state.statuses[rootID] = append(state.statuses[rootID], value)
				}
			}
		}
	}
	return state, nil
}

func descriptorParentReport(descriptor map[string]any) string {
	properties, _ := descriptor["ValueProperties"].(map[string]any)
	value, _ := properties["ParentTitleFields"].(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if index := strings.LastIndex(value, " : "); index >= 0 {
		return strings.TrimSpace(value[index+3:])
	}
	return value
}

func addStringSet(sets map[string]map[string]bool, key, value string) {
	if sets[key] == nil {
		sets[key] = make(map[string]bool)
	}
	sets[key][value] = true
}

func onlyStringKey[V any](values map[string]V) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	for key := range values {
		return key, true
	}
	return "", false
}

func uniqueCatalogTarget(catalog *targetCatalog, rootID, name string) (string, bool) {
	if catalog == nil {
		return "", false
	}
	var matched string
	for id, model := range catalog.models {
		if catalog.ambiguous[id] || model.rootID != rootID || model.name != name {
			continue
		}
		if matched != "" && matched != id {
			return "", false
		}
		matched = id
	}
	return matched, matched != ""
}

func receiptHasForbiddenCommand(exchanges []exchange, catalog *targetCatalog) bool {
	for i := range exchanges {
		for _, command := range exchanges[i].commands {
			if forbiddenReceiptName(command.CommandName) {
				return true
			}
			if target, ok := catalog.lookup(command.TargetID); ok && forbiddenReceiptName(target.name) {
				return true
			}
		}
	}
	return false
}

func forbiddenReceiptName(value string) bool {
	normalized := normalizeControlName(value)
	for _, word := range []string{"submit", "approve", "approval", "post", "workflow", "recall"} {
		if strings.Contains(normalized, word) {
			return true
		}
	}
	return false
}

func responseHasExpectedReceiptDialog(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	roots := make(map[string]bool)
	var walk func(any, string)
	walk = func(current any, dialogRoot string) {
		switch current := current.(type) {
		case map[string]any:
			id, _ := current["Id"].(string)
			name, _ := current["Name"].(string)
			if name == receiptDialogFormName && id != "" {
				dialogRoot = id
				roots[id] = false
			}
			if dialogRoot != "" && (name == receiptUploadControlName || name == receiptOKControlName) {
				if roots[dialogRoot] || name == receiptOKControlName {
					roots[dialogRoot] = true
				}
			}
			for _, child := range current {
				walk(child, dialogRoot)
			}
		case []any:
			for _, child := range current {
				walk(child, dialogRoot)
			}
		}
	}
	walk(value, "")
	if len(roots) != 1 {
		return false
	}
	for root := range roots {
		return roots[root] && recursiveNamedModelExists(value, root, receiptUploadControlName) && recursiveNamedModelExists(value, root, receiptOKControlName)
	}
	return false
}

func recursiveNamedModelExists(value any, rootID, wanted string) bool {
	found := false
	var walk func(any, string)
	walk = func(current any, root string) {
		if found {
			return
		}
		switch current := current.(type) {
		case map[string]any:
			if explicit, _ := current["RootId"].(string); explicit != "" {
				root = explicit
			}
			if id, _ := current["Id"].(string); id == rootID {
				if name, _ := current["Name"].(string); name == receiptDialogFormName {
					root = rootID
				}
			}
			if root == rootID {
				if name, _ := current["Name"].(string); name == wanted {
					found = true
					return
				}
			}
			for _, child := range current {
				walk(child, root)
			}
		case []any:
			for _, child := range current {
				walk(child, root)
			}
		}
	}
	walk(value, "")
	return found
}

func findReceiptCheckFileFlow(exchanges []exchange, catalog *targetCatalog) (receiptCommandKey, int, error) {
	keys := make(map[receiptCommandKey]int)
	for i := range exchanges {
		ex := &exchanges[i]
		if !exchangeSucceeded(ex) {
			continue
		}
		for _, command := range ex.commands {
			if command.CommandName != "CheckFile" {
				continue
			}
			target, ok := catalog.lookup(command.TargetID)
			if !ok || target.name != receiptUploadControlName || target.rootID != command.RootID {
				continue
			}
			key := receiptCommandKey{rootID: command.RootID, targetID: command.TargetID}
			keys[key] = ex.entryIndex
		}
	}
	if len(keys) != 1 {
		return receiptCommandKey{}, 0, errors.New("receipt HAR lacks one unambiguous UploadControl CheckFile flow")
	}
	for key, entryIndex := range keys {
		return key, entryIndex, nil
	}
	panic("unreachable")
}

func findReceiptUpload(entries []harEntry, exchanges []exchange, session SessionProfile, afterEntry int) (receiptUploadObservation, error) {
	origin, err := url.Parse(session.BaseURL)
	if err != nil {
		return receiptUploadObservation{}, errors.New("receipt capture base URL is invalid")
	}
	wantOrigin := strings.ToLower(origin.Scheme) + "://" + strings.ToLower(origin.Host)
	wantFingerprint := receiptAuthFingerprint(exchanges[0].headers, exchanges[0].cookies)
	var matched *receiptUploadObservation
	for entryIndex, entry := range entries {
		if entryIndex <= afterEntry || !strings.EqualFold(entry.Request.Method, http.MethodPost) {
			continue
		}
		u, err := url.Parse(entry.Request.URL)
		if err != nil || !u.IsAbs() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != receiptUploadEndpointPath {
			continue
		}
		if strings.ToLower(u.Scheme)+"://"+strings.ToLower(u.Host) != wantOrigin {
			return receiptUploadObservation{}, errors.New("receipt upload origin does not match the Dynamics session")
		}
		if receiptAuthFingerprint(entry.Request.Headers, entry.Request.Cookies) != wantFingerprint {
			return receiptUploadObservation{}, errors.New("receipt upload does not belong to the coherent authenticated session")
		}
		if entry.Response.Status < 200 || entry.Response.Status >= 300 {
			return receiptUploadObservation{}, errors.New("receipt upload POST was not successful")
		}
		observation, err := parseReceiptUploadEntry(entryIndex, entry, session.Company)
		if err != nil {
			return receiptUploadObservation{}, err
		}
		if matched != nil {
			return receiptUploadObservation{}, errors.New("receipt HAR contains multiple successful upload POST flows")
		}
		matched = &observation
	}
	if matched == nil {
		return receiptUploadObservation{}, errors.New("receipt HAR lacks a successful POST to /filemanagement")
	}
	return *matched, nil
}

func receiptAuthFingerprint(headers []harNameValue, cookies []harCookie) string {
	components := make(map[string]bool)
	for _, header := range headers {
		name := strings.ToLower(strings.TrimSpace(header.Name))
		switch name {
		case "ms-dyn-bsid", "ms-dyn-csrftoken":
			components["header:"+name+"="+header.Value] = true
		case "cookie":
			request := &http.Request{Header: http.Header{"Cookie": []string{header.Value}}}
			for _, cookie := range request.Cookies() {
				if isSessionCookie(cookie.Name) {
					components["cookie:"+strings.ToLower(cookie.Name)+"="+cookie.Value] = true
				}
			}
		}
	}
	for _, cookie := range cookies {
		if isSessionCookie(cookie.Name) {
			components["cookie:"+strings.ToLower(cookie.Name)+"="+cookie.Value] = true
		}
	}
	ordered := make([]string, 0, len(components))
	for component := range components {
		ordered = append(ordered, component)
	}
	slices.Sort(ordered)
	hash := sha256.New()
	for _, component := range ordered {
		fmt.Fprintf(hash, "%d:%s\n", len(component), component)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func parseReceiptUploadEntry(entryIndex int, entry harEntry, company string) (receiptUploadObservation, error) {
	contentType := ""
	for _, header := range entry.Request.Headers {
		if strings.EqualFold(header.Name, "Content-Type") {
			contentType = header.Value
			break
		}
	}
	mediaType, parameters, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") || parameters["boundary"] == "" {
		return receiptUploadObservation{}, errors.New("receipt upload is not valid multipart/form-data")
	}
	reader := multipart.NewReader(strings.NewReader(entry.Request.PostData.Text), parameters["boundary"])
	fieldOrder := make([]string, 0, len(receiptMultipartFieldOrder))
	values := make(map[string]string)
	fileParts := 0
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return receiptUploadObservation{}, errors.New("receipt upload multipart body is invalid")
		}
		name := part.FormName()
		fieldOrder = append(fieldOrder, name)
		if part.FileName() != "" {
			fileParts++
			if _, err := io.Copy(io.Discard, part); err != nil {
				return receiptUploadObservation{}, errors.New("receipt upload file part is unreadable")
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, 1<<20))
		if err != nil {
			return receiptUploadObservation{}, errors.New("receipt upload multipart field is unreadable")
		}
		values[name] = string(data)
	}
	if !slices.Equal(fieldOrder, receiptMultipartFieldOrder) || fileParts != 1 {
		return receiptUploadObservation{}, errors.New("receipt upload multipart field order is not the observed contract")
	}
	if strings.TrimSpace(values["clientId"]) == "" || isRedacted(values["clientId"]) ||
		strings.TrimSpace(values["accesstoken"]) == "" || isRedacted(values["accesstoken"]) {
		return receiptUploadObservation{}, errors.New("receipt upload has missing or redacted transient credentials")
	}
	maxChunkSize, err := strconv.ParseInt(strings.TrimSpace(values["maxChunkSize"]), 10, 64)
	if err != nil || maxChunkSize <= 0 {
		return receiptUploadObservation{}, errors.New("receipt upload max chunk size is invalid")
	}
	if values["companyid"] != company || values["docutypeid"] != receiptDocumentType || values["ischunked"] != "false" {
		return receiptUploadObservation{}, errors.New("receipt upload metadata does not match the observed single-file contract")
	}
	if strings.TrimSpace(values["tableid"]) == "" || strings.TrimSpace(values["recid"]) == "" || strings.TrimSpace(values["docuname"]) == "" {
		return receiptUploadObservation{}, errors.New("receipt upload metadata is incomplete")
	}
	var response []struct {
		FileID string `json:"fileId"`
	}
	body, err := decodeHARContent(entry.Response.Content.Text, entry.Response.Content.Encoding)
	if err != nil || json.Unmarshal(body, &response) != nil || len(response) != 1 || strings.TrimSpace(response[0].FileID) == "" || isRedacted(response[0].FileID) {
		return receiptUploadObservation{}, errors.New("receipt upload response lacks one usable file identifier")
	}
	return receiptUploadObservation{
		entryIndex:   entryIndex,
		fieldOrder:   fieldOrder,
		maxChunkSize: maxChunkSize,
		documentType: values["docutypeid"],
		fileID:       response[0].FileID,
	}, nil
}

func findReceiptCloseFlow(exchanges []exchange, key receiptCommandKey, upload receiptUploadObservation) (int, error) {
	matched := -1
	for i := range exchanges {
		ex := &exchanges[i]
		if ex.entryIndex <= upload.entryIndex || !exchangeSucceeded(ex) {
			continue
		}
		if !requestHasUploadedFileClosePair(ex.request, key, upload.fileID) {
			continue
		}
		if matched >= 0 {
			return 0, errors.New("receipt HAR contains multiple UploadedFileId and CloseDialog flows")
		}
		matched = i
	}
	if matched < 0 {
		return 0, errors.New("receipt HAR lacks UploadedFileId followed by CloseDialog")
	}
	return matched, nil
}

func requestHasUploadedFileClosePair(envelope wireEnvelope, key receiptCommandKey, fileID string) bool {
	for _, message := range envelope.Messages {
		for i := 0; i+1 < len(message.Interactions); i++ {
			var property struct {
				Type         string          `json:"$type"`
				RootID       string          `json:"RootId"`
				TargetID     string          `json:"TargetId"`
				PropertyName string          `json:"PropertyName"`
				NewValue     json.RawMessage `json:"NewValue"`
			}
			if json.Unmarshal(message.Interactions[i], &property) != nil ||
				!strings.HasSuffix(strings.ToLower(property.Type), "propertychangeinteraction") ||
				property.RootID != key.rootID || property.TargetID != key.targetID || property.PropertyName != receiptUploadedFileIDProperty {
				continue
			}
			var value string
			if json.Unmarshal(property.NewValue, &value) != nil || value == "" || value != fileID {
				continue
			}
			var command wireCommand
			if json.Unmarshal(message.Interactions[i+1], &command) == nil && command.CommandName == "CloseDialog" && command.RootID == key.rootID && command.TargetID == key.targetID {
				return true
			}
		}
	}
	return false
}

func findReceiptOKFlow(exchanges []exchange, catalog *targetCatalog, dialogRoot string, afterExchange int) (int, string, int64, error) {
	matched := -1
	matchedRoot := ""
	var matchedCount int64
	for i := afterExchange + 1; i < len(exchanges); i++ {
		ex := &exchanges[i]
		if !exchangeSucceeded(ex) {
			continue
		}
		for _, command := range ex.commands {
			if command.CommandName != "Click" || command.RootID != dialogRoot {
				continue
			}
			target, ok := catalog.lookup(command.TargetID)
			if !ok || target.name != receiptOKControlName || target.rootID != dialogRoot {
				continue
			}
			root, count, ok := positiveReceiptCountUpdate(ex.responseBody)
			if !ok {
				continue
			}
			if matched >= 0 {
				return 0, "", 0, errors.New("receipt HAR contains multiple successful OK receipt flows")
			}
			matched, matchedRoot, matchedCount = i, root, count
		}
	}
	if matched < 0 {
		return 0, "", 0, errors.New("receipt HAR lacks an OK click with a positive ReceiptCount update")
	}
	return matched, matchedRoot, matchedCount, nil
}

func positiveReceiptCountUpdate(body []byte) (string, int64, bool) {
	var envelope wireEnvelope
	if json.Unmarshal(body, &envelope) != nil {
		return "", 0, false
	}
	matchedRoot := ""
	var matchedCount int64
	for _, message := range envelope.Messages {
		for _, raw := range message.Interactions {
			var interaction map[string]any
			if json.Unmarshal(raw, &interaction) != nil {
				continue
			}
			rootID, _ := interaction["RootId"].(string)
			descriptor, _ := interaction["Descriptor"].(map[string]any)
			if descriptor == nil {
				continue
			}
			values := recursiveNamedValues(descriptor, receiptCountControlName)
			for _, value := range values {
				count, err := strconv.ParseInt(value, 10, 64)
				if err != nil || count <= 0 || matchedRoot != "" {
					return "", 0, false
				}
				matchedRoot, matchedCount = rootID, count
			}
		}
	}
	return matchedRoot, matchedCount, matchedRoot != ""
}

func recursiveNamedValues(value any, name string) []string {
	var values []string
	var walk func(any)
	walk = func(current any) {
		switch current := current.(type) {
		case map[string]any:
			if currentName, _ := current["Name"].(string); currentName == name {
				properties, _ := current["ValueProperties"].(map[string]any)
				if value, _ := properties["Value"].(string); value != "" {
					values = append(values, value)
				}
			}
			for _, child := range current {
				walk(child)
			}
		case []any:
			for _, child := range current {
				walk(child)
			}
		}
	}
	walk(value)
	return values
}

func recursivePropertyStrings(value any, propertyName string) []string {
	var values []string
	var walk func(any)
	walk = func(current any) {
		switch current := current.(type) {
		case map[string]any:
			if properties, ok := current["Properties"].(map[string]any); ok {
				if value, ok := properties[propertyName].(string); ok {
					values = append(values, value)
				}
			}
			for _, child := range current {
				walk(child)
			}
		case []any:
			for _, child := range current {
				walk(child)
			}
		}
	}
	walk(value)
	return values
}

func statusesForRootBeforeOrAt(exchanges []exchange, rootID string, through int) []string {
	var values []string
	for i := 0; i <= through && i < len(exchanges); i++ {
		ex := &exchanges[i]
		if ex.response == nil {
			continue
		}
		for _, message := range ex.response.Messages {
			for _, raw := range message.Interactions {
				var interaction map[string]any
				if json.Unmarshal(raw, &interaction) != nil {
					continue
				}
				root, _ := interaction["RootId"].(string)
				if root != rootID {
					continue
				}
				descriptor, _ := interaction["Descriptor"].(map[string]any)
				values = append(values, recursivePropertyStrings(descriptor, "expenseReportStatus_dataMethod")...)
			}
		}
	}
	return values
}

func requireExactDraftStatus(values []string) error {
	if len(values) == 0 {
		return errors.New("receipt HAR lacks exact Draft status evidence")
	}
	for _, value := range values {
		if value != "Draft" {
			return errors.New("receipt HAR report status is not exactly Draft")
		}
	}
	return nil
}

func receiptCountIncreased(exchanges []exchange, rootID string, at int, after int64) bool {
	var before int64
	hasBefore := false
	for i := 0; i < at; i++ {
		root, count, ok := positiveReceiptCountUpdate(exchanges[i].responseBody)
		if ok && root == rootID {
			if !hasBefore || count > before {
				before, hasBefore = count, true
			}
		}
	}
	if hasBefore {
		return after > before
	}
	return after > 0
}

func findReceiptSaveFlow(exchanges []exchange, catalog *targetCatalog, detailsRoot string, afterExchange int) (int, error) {
	matched := -1
	for i := afterExchange + 1; i < len(exchanges); i++ {
		ex := &exchanges[i]
		if !exchangeSucceeded(ex) {
			continue
		}
		for _, command := range ex.commands {
			if command.CommandName != "Click" || command.RootID != detailsRoot {
				continue
			}
			if target, ok := catalog.lookup(command.TargetID); ok && (target.name != receiptSaveControlName || target.rootID != detailsRoot) {
				continue
			}
			if !responseDeletesRoot(ex.responseBody, detailsRoot) {
				continue
			}
			if matched >= 0 {
				return 0, errors.New("receipt HAR contains multiple SaveAndClose flows")
			}
			matched = i
		}
	}
	if matched < 0 {
		return 0, errors.New("receipt HAR lacks SaveAndClose after the receipt-count increase")
	}
	return matched, nil
}

func responseDeletesRoot(body []byte, rootID string) bool {
	var envelope wireEnvelope
	if json.Unmarshal(body, &envelope) != nil {
		return false
	}
	for _, message := range envelope.Messages {
		for _, raw := range message.Interactions {
			var interaction struct {
				Type     string `json:"$type"`
				RootID   string `json:"RootId"`
				TargetID string `json:"TargetId"`
			}
			if json.Unmarshal(raw, &interaction) == nil && strings.HasSuffix(strings.ToLower(interaction.Type), "deleteviewmodelinteraction") && interaction.RootID == rootID && interaction.TargetID == rootID {
				return true
			}
		}
	}
	return false
}

func responseContainsReportNumber(body []byte, reportNumber string) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	found := false
	var walk func(any)
	walk = func(current any) {
		if found {
			return
		}
		switch current := current.(type) {
		case map[string]any:
			if properties, ok := current["Properties"].(map[string]any); ok {
				if value, _ := properties["ExpNumber_field"].(string); value == reportNumber {
					found = true
					return
				}
			}
			for _, child := range current {
				walk(child)
			}
		case []any:
			for _, child := range current {
				walk(child)
			}
		}
	}
	walk(value)
	return found
}
