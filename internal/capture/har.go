package capture

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const processMessagesPath = "/Services/ReliableCommunicationManager.svc/ProcessMessages"

type harDocument struct {
	Log struct {
		Version string     `json:"version"`
		Entries []harEntry `json:"entries"`
	} `json:"log"`
}

type harEntry struct {
	Request  harRequest  `json:"request"`
	Response harResponse `json:"response"`
}

type harRequest struct {
	Method   string         `json:"method"`
	URL      string         `json:"url"`
	Headers  []harNameValue `json:"headers"`
	Cookies  []harCookie    `json:"cookies"`
	PostData struct {
		Text string `json:"text"`
	} `json:"postData"`
}

type harResponse struct {
	Status  int `json:"status"`
	Content struct {
		Text     string `json:"text"`
		Encoding string `json:"encoding"`
	} `json:"content"`
}

type harNameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Path     string `json:"path"`
	Domain   string `json:"domain"`
	Expires  string `json:"expires"`
	HTTPOnly bool   `json:"httpOnly"`
	Secure   bool   `json:"secure"`
	SameSite string `json:"sameSite"`
}

type wireEnvelope struct {
	ChannelID                      *int64        `json:"ChannelId"`
	CompanyID                      string        `json:"CompanyId"`
	Language                       string        `json:"Language"`
	LastAcknowledgedSequenceNumber *int64        `json:"LastAcknowledgedSequenceNumber"`
	Messages                       []wireMessage `json:"Messages"`
}

type wireMessage struct {
	SequenceNumber *int64            `json:"SequenceNumber"`
	Interactions   []json.RawMessage `json:"Interactions"`
}

type wireCommand struct {
	Type                 string            `json:"$type"`
	CallbackID           string            `json:"CallbackId"`
	FailureCallbackID    string            `json:"FailureCallbackId"`
	CommandName          string            `json:"CommandName"`
	RootID               string            `json:"RootId"`
	TargetID             string            `json:"TargetId"`
	PositionalParameters []json.RawMessage `json:"PositionalParameters"`
}

type wireCallback struct {
	Type             string `json:"$type"`
	CallbackID       string `json:"CallbackId"`
	UnusedCallbackID string `json:"UnusedCallbackId"`
}

type observedCommand struct {
	wireCommand
	messageIndex     int
	interactionIndex int
}

type exchange struct {
	entryIndex         int
	endpoint           *url.URL
	request            wireEnvelope
	response           *wireEnvelope
	responseBody       []byte
	responseParseErr   error
	status             int
	headers            []harNameValue
	cookies            []harCookie
	commands           []observedCommand
	callbackIDs        map[string]bool
	maxRequestSequence int64
}

type exchangeGroupKey struct {
	origin             string
	endpoint           string
	channel            int64
	company            string
	language           string
	sessionFingerprint string
}

type targetModel struct {
	name     string
	rootID   string
	rootName string
}

type targetCatalog struct {
	models    map[string]targetModel
	ambiguous map[string]bool
}

func parseHAR(r io.Reader) (*Profile, error) {
	exchanges, err := decodeHARExchanges(r)
	if err != nil {
		return nil, err
	}

	selected, err := selectExchangeGroup(exchanges)
	if err != nil {
		return nil, err
	}

	profile, err := extractProfile(selected)
	if err != nil {
		return nil, err
	}
	return profile, nil
}

func parseBootstrapSessionHAR(r io.Reader) (*BootstrapProfile, error) {
	exchanges, err := decodeHARExchanges(r)
	if err != nil {
		return nil, err
	}
	selected, err := selectLatestBootstrapGroup(exchanges)
	if err != nil {
		return nil, err
	}
	session, err := extractSessionProfile(selected)
	if err != nil {
		return nil, err
	}
	return &BootstrapProfile{Session: session}, nil
}

func parseBootstrapHAR(r io.Reader) (*BootstrapProfile, error) {
	exchanges, err := decodeHARExchanges(r)
	if err != nil {
		return nil, err
	}

	selected, err := selectLatestBootstrapGroup(exchanges)
	if err != nil {
		return nil, err
	}
	session, err := extractSessionProfile(selected)
	if err != nil {
		return nil, err
	}
	newReport, err := extractWorkspaceNewReport(selected)
	if err != nil {
		return nil, err
	}
	return &BootstrapProfile{Session: session, NewReport: newReport}, nil
}

func decodeHARExchanges(r io.Reader) ([]exchange, error) {
	if r == nil {
		return nil, errors.New("HAR reader is nil")
	}

	decoder := json.NewDecoder(r)
	var document harDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode HAR: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("decode HAR: trailing JSON value")
		}
		return nil, fmt.Errorf("decode HAR trailing data: %w", err)
	}
	if document.Log.Version != "1.2" {
		return nil, fmt.Errorf("unsupported HAR version %q; want 1.2", document.Log.Version)
	}

	exchanges, err := collectExchanges(document.Log.Entries)
	if err != nil {
		return nil, err
	}
	if len(exchanges) == 0 {
		return nil, errors.New("HAR contains no Dynamics ProcessMessages requests")
	}
	return exchanges, nil
}

func collectExchanges(entries []harEntry) ([]exchange, error) {
	exchanges := make([]exchange, 0)
	for entryIndex, entry := range entries {
		endpoint, ok := processMessagesURL(entry.Request.Method, entry.Request.URL)
		if !ok {
			continue
		}
		if strings.TrimSpace(entry.Request.PostData.Text) == "" {
			return nil, fmt.Errorf("ProcessMessages entry %d has no request body", entryIndex)
		}

		var request wireEnvelope
		if err := json.Unmarshal([]byte(entry.Request.PostData.Text), &request); err != nil {
			return nil, fmt.Errorf("decode ProcessMessages request at entry %d: %w", entryIndex, err)
		}
		if request.ChannelID == nil || request.LastAcknowledgedSequenceNumber == nil {
			return nil, fmt.Errorf("ProcessMessages request at entry %d is missing sequence metadata", entryIndex)
		}

		ex := exchange{
			entryIndex:  entryIndex,
			endpoint:    endpoint,
			request:     request,
			status:      entry.Response.Status,
			headers:     entry.Request.Headers,
			cookies:     entry.Request.Cookies,
			callbackIDs: make(map[string]bool),
		}
		ex.commands = commandsIn(request)
		for _, message := range request.Messages {
			if message.SequenceNumber == nil {
				return nil, fmt.Errorf("ProcessMessages request at entry %d has a message without a sequence number", entryIndex)
			}
			if *message.SequenceNumber > ex.maxRequestSequence {
				ex.maxRequestSequence = *message.SequenceNumber
			}
		}

		if entry.Response.Status >= 200 && entry.Response.Status < 300 {
			body, err := decodeHARContent(entry.Response.Content.Text, entry.Response.Content.Encoding)
			body = trimJSONBOM(body)
			if err != nil {
				ex.responseParseErr = fmt.Errorf("decode ProcessMessages response content at entry %d: %w", entryIndex, err)
			} else if len(bytes.TrimSpace(body)) == 0 {
				ex.responseParseErr = fmt.Errorf("successful ProcessMessages response at entry %d has no body", entryIndex)
			} else {
				var response wireEnvelope
				if err := json.Unmarshal(body, &response); err != nil {
					ex.responseParseErr = fmt.Errorf("decode ProcessMessages response at entry %d: %w", entryIndex, err)
				} else {
					ex.response = &response
					ex.responseBody = body
					ex.callbackIDs = callbackIDsIn(response)
				}
			}
		}
		exchanges = append(exchanges, ex)
	}
	return exchanges, nil
}

func selectExchangeGroup(exchanges []exchange) ([]exchange, error) {
	if len(exchanges) == 0 {
		return nil, errors.New("HAR contains no Dynamics ProcessMessages requests")
	}

	selectedKey := exchangeKey(exchanges[0])
	for i := 1; i < len(exchanges); i++ {
		if exchangeKey(exchanges[i]) == selectedKey {
			continue
		}
		return nil, fmt.Errorf(
			"HAR contains mixed ProcessMessages sessions at entries %d and %d; capture exactly one contiguous origin/endpoint/channel/company/language flow",
			exchanges[i-1].entryIndex,
			exchanges[i].entryIndex,
		)
	}
	return exchanges, nil
}

func selectLatestBootstrapGroup(exchanges []exchange) ([]exchange, error) {
	if len(exchanges) == 0 {
		return nil, errors.New("HAR contains no Dynamics ProcessMessages requests")
	}

	last := len(exchanges) - 1
	selectedKey := bootstrapExchangeKey(exchanges[last])
	first := last
	for first > 0 && bootstrapExchangeKey(exchanges[first-1]) == selectedKey {
		first--
	}
	selected := exchanges[first:]
	tail := &selected[len(selected)-1]
	if tail.responseParseErr != nil {
		return nil, fmt.Errorf("latest ProcessMessages session response is unusable: %w", tail.responseParseErr)
	}
	if !exchangeSucceeded(tail) {
		return nil, fmt.Errorf("latest ProcessMessages session does not end in a successful acknowledged response at entry %d", tail.entryIndex)
	}
	return selected, nil
}

func bootstrapExchangeKey(ex exchange) exchangeGroupKey {
	key := exchangeKey(ex)
	key.sessionFingerprint = ""
	return key
}

func exchangeKey(ex exchange) exchangeGroupKey {
	endpoint := ""
	origin := ""
	if ex.endpoint != nil {
		normalized := *ex.endpoint
		normalized.Scheme = strings.ToLower(normalized.Scheme)
		normalized.Host = strings.ToLower(normalized.Host)
		endpoint = normalized.String()
		origin = normalized.Scheme + "://" + normalized.Host
	}
	channel := int64(-1)
	if ex.request.ChannelID != nil {
		channel = *ex.request.ChannelID
	}
	return exchangeGroupKey{
		origin:             origin,
		endpoint:           endpoint,
		channel:            channel,
		company:            ex.request.CompanyID,
		language:           ex.request.Language,
		sessionFingerprint: exchangeSessionFingerprint(ex),
	}
}

// exchangeSessionFingerprint binds a flow to one authenticated web-client
// session without retaining or reporting credential values. Browser session and
// CSRF headers plus the authentication cookie chunks are stable across the
// captured flow but change when a HAR crosses a login/reload session boundary.
func exchangeSessionFingerprint(ex exchange) string {
	components := make(map[string]struct{})
	for _, header := range ex.headers {
		name := strings.ToLower(strings.TrimSpace(header.Name))
		switch name {
		case "authorization", "ms-dyn-bsid", "ms-dyn-csrftoken", "ms-dyn-sid":
			components["header:"+name+"="+header.Value] = struct{}{}
		case "cookie":
			request := &http.Request{Header: http.Header{"Cookie": []string{header.Value}}}
			for _, cookie := range request.Cookies() {
				if isSessionCookie(cookie.Name) {
					components["cookie:"+strings.ToLower(cookie.Name)+"="+cookie.Value] = struct{}{}
				}
			}
		}
	}
	for _, cookie := range ex.cookies {
		if isSessionCookie(cookie.Name) {
			components["cookie:"+strings.ToLower(cookie.Name)+"="+cookie.Value] = struct{}{}
		}
	}

	ordered := make([]string, 0, len(components))
	for component := range components {
		ordered = append(ordered, component)
	}
	sort.Strings(ordered)
	hash := sha256.New()
	for _, component := range ordered {
		fmt.Fprintf(hash, "%d:%s\n", len(component), component)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func isSessionCookie(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return normalized == "ms-dyn-csrftoken" || strings.HasPrefix(normalized, "dynamicsowinauth")
}

func extractProfile(exchanges []exchange) (*Profile, error) {
	session, err := extractSessionProfile(exchanges)
	if err != nil {
		return nil, err
	}
	flow, err := extractDraftFlow(exchanges)
	if err != nil {
		return nil, err
	}
	return &Profile{Session: session, Draft: flow}, nil
}

func extractSessionProfile(exchanges []exchange) (SessionProfile, error) {
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
		return SessionProfile{}, errors.New("HAR contains no successful Dynamics ProcessMessages requests")
	}
	if maxClientSequence > math.MaxInt64-4 {
		return SessionProfile{}, errors.New("captured client sequence lacks headroom for draft creation")
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

	// Credential validation is intentionally performed before response-body and
	// flow validation so sanitized captures fail with an actionable error.
	if err := validateCredentials(session.RequestHeaders, session.Cookies); err != nil {
		return SessionProfile{}, err
	}
	for i := range exchanges {
		if exchanges[i].responseParseErr != nil {
			return SessionProfile{}, exchanges[i].responseParseErr
		}
	}
	return session, nil
}

func processMessagesURL(method, rawURL string) (*url.URL, bool) {
	if !strings.EqualFold(strings.TrimSpace(method), http.MethodPost) {
		return nil, false
	}
	u, err := url.Parse(rawURL)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return nil, false
	}
	path := strings.TrimSuffix(u.Path, "/")
	if !strings.HasSuffix(strings.ToLower(path), strings.ToLower(processMessagesPath)) {
		return nil, false
	}
	return u, true
}

func baseURLForEndpoint(endpoint *url.URL) (string, error) {
	if endpoint == nil {
		return "", errors.New("capture endpoint URL is missing")
	}
	lowerPath := strings.ToLower(endpoint.Path)
	index := strings.LastIndex(lowerPath, strings.ToLower(processMessagesPath))
	if index < 0 {
		return "", errors.New("capture endpoint path is invalid")
	}
	base := &url.URL{
		Scheme: endpoint.Scheme,
		Host:   endpoint.Host,
		Path:   strings.TrimRight(endpoint.Path[:index], "/"),
	}
	return base.String(), nil
}

func commandsIn(envelope wireEnvelope) []observedCommand {
	var commands []observedCommand
	for messageIndex, message := range envelope.Messages {
		for interactionIndex, raw := range message.Interactions {
			var command wireCommand
			if err := json.Unmarshal(raw, &command); err != nil || strings.TrimSpace(command.CommandName) == "" {
				continue
			}
			if command.Type != "" && !strings.HasSuffix(strings.ToLower(command.Type), "commandinteraction") {
				continue
			}
			commands = append(commands, observedCommand{
				wireCommand:      command,
				messageIndex:     messageIndex,
				interactionIndex: interactionIndex,
			})
		}
	}
	return commands
}

func callbackIDsIn(envelope wireEnvelope) map[string]bool {
	ids := make(map[string]bool)
	for _, message := range envelope.Messages {
		for _, raw := range message.Interactions {
			var callback wireCallback
			if err := json.Unmarshal(raw, &callback); err != nil || callback.CallbackID == "" {
				continue
			}
			if callback.Type == "" || strings.HasSuffix(strings.ToLower(callback.Type), "commandcallbackinteraction") {
				ids[callback.CallbackID] = true
			}
		}
	}
	return ids
}

func extractWorkspaceNewReport(exchanges []exchange) (CommandTarget, error) {
	catalog := newTargetCatalog()
	activeRoots := make(map[string]bool)
	for i := range exchanges {
		ex := &exchanges[i]
		if !exchangeSucceeded(ex) {
			continue
		}
		catalog.collect(ex.responseBody)
		if err := updateActiveWorkspaceRoots(activeRoots, ex.response); err != nil {
			return CommandTarget{}, fmt.Errorf("inspect Expense workspace response at entry %d: %w", ex.entryIndex, err)
		}
	}

	rootID, ok := onlyStringKey(activeRoots)
	if !ok {
		if len(activeRoots) == 0 {
			return extractObservedWorkspaceOpen(exchanges)
		}
		return CommandTarget{}, errors.New("latest ProcessMessages session leaves multiple active ExpenseWorkspace_form roots")
	}
	root, ok := catalog.lookup(rootID)
	if !ok || !strings.EqualFold(root.name, "ExpenseWorkspace_form") || root.rootID != rootID || !strings.EqualFold(root.rootName, "ExpenseWorkspace_form") {
		return CommandTarget{}, errors.New("latest ProcessMessages session has an ambiguous ExpenseWorkspace_form root")
	}

	targetID, ok := uniqueCatalogTarget(catalog, rootID, "NewExpenseReportReportsTab")
	if !ok {
		return CommandTarget{}, errors.New("latest ProcessMessages session does not expose exactly one NewExpenseReportReportsTab in ExpenseWorkspace_form")
	}
	target, ok := catalog.lookup(targetID)
	if !ok || target.rootID != rootID || !strings.EqualFold(target.rootName, "ExpenseWorkspace_form") {
		return CommandTarget{}, errors.New("latest ProcessMessages session new-report control is not bound to ExpenseWorkspace_form")
	}

	return CommandTarget{
		CommandName: "Click",
		RootID:      rootID,
		TargetID:    targetID,
		ControlName: "NewExpenseReportReportsTab",
	}, nil
}

func extractObservedWorkspaceOpen(exchanges []exchange) (CommandTarget, error) {
	var matches []CommandTarget
	for i := range exchanges {
		ex := &exchanges[i]
		if !exchangeSucceeded(ex) {
			continue
		}
		responseCatalog := newTargetCatalog()
		responseCatalog.collect(ex.responseBody)
		var dialogID string
		for id, model := range responseCatalog.models {
			if responseCatalog.ambiguous[id] || !strings.EqualFold(model.name, "ExpenseNewExpenseReport_form") || model.rootID != id {
				continue
			}
			if dialogID != "" && dialogID != id {
				dialogID = ""
				break
			}
			dialogID = id
		}
		if dialogID == "" {
			continue
		}
		purposeFound := false
		for id, model := range responseCatalog.models {
			if responseCatalog.ambiguous[id] {
				continue
			}
			if strings.EqualFold(model.name, "NamePurpose") && model.rootID == dialogID {
				purposeFound = true
				break
			}
		}
		if !purposeFound {
			continue
		}
		for commandIndex, command := range ex.commands {
			if !strings.EqualFold(command.CommandName, "Click") || !isStrictNewReportPair(ex.commands, commandIndex) {
				continue
			}
			matches = append(matches, commandTarget(command, "NewExpenseReportReportsTab"))
		}
	}
	if len(matches) != 1 {
		return CommandTarget{}, errors.New("latest ProcessMessages session does not leave an active ExpenseWorkspace_form or exactly one validated New expense report open request")
	}
	return matches[0], nil
}

func updateActiveWorkspaceRoots(active map[string]bool, response *wireEnvelope) error {
	if response == nil {
		return nil
	}
	for _, message := range response.Messages {
		for _, raw := range message.Interactions {
			var interaction struct {
				Type       string `json:"$type"`
				RootID     string `json:"RootId"`
				TargetID   string `json:"TargetId"`
				Descriptor struct {
					ID   string `json:"Id"`
					Name string `json:"Name"`
				} `json:"Descriptor"`
			}
			if err := json.Unmarshal(raw, &interaction); err != nil {
				return fmt.Errorf("decode response interaction: %w", err)
			}
			typeName := strings.ToLower(interaction.Type)
			if strings.HasSuffix(typeName, "deleteviewmodelinteraction") {
				delete(active, interaction.TargetID)
				continue
			}
			if !strings.HasSuffix(typeName, "createviewmodelinteraction") && !strings.HasSuffix(typeName, "updateviewmodelinteraction") {
				continue
			}
			if !strings.EqualFold(interaction.Descriptor.Name, "ExpenseWorkspace_form") {
				continue
			}
			rootID := interaction.Descriptor.ID
			if rootID == "" {
				rootID = interaction.RootID
			}
			if rootID != "" {
				active[rootID] = true
			}
		}
	}
	return nil
}

func extractDraftFlow(exchanges []exchange) (DraftFlow, error) {
	type commandCandidate struct {
		exchangeIndex int
		command       observedCommand
		controlName   string
	}
	type createCandidate struct {
		exchangeIndex     int
		setValue          observedCommand
		setControlName    string
		invokeDefault     observedCommand
		invokeControlName string
	}

	var newReports []commandCandidate
	var creates []createCandidate
	var saves []commandCandidate
	catalog := newTargetCatalog()
	// Build the target catalog from the complete coherent flow first. The
	// workspace's new-report control is serialized again after SaveAndClose,
	// while the dialog and details controls appear in earlier responses.
	for exchangeIndex := range exchanges {
		if exchanges[exchangeIndex].response != nil {
			catalog.collect(exchanges[exchangeIndex].responseBody)
		}
	}

	for exchangeIndex := range exchanges {
		ex := &exchanges[exchangeIndex]
		if exchangeSucceeded(ex) {
			for commandIndex, command := range ex.commands {
				if !strings.EqualFold(command.CommandName, "Click") {
					continue
				}

				target, targetOK := catalog.lookup(command.TargetID)
				root, rootOK := catalog.lookup(command.RootID)
				if targetOK && rootOK &&
					strings.EqualFold(target.name, "NewExpenseReportReportsTab") &&
					target.rootID == command.RootID &&
					strings.EqualFold(root.name, "ExpenseWorkspace_form") &&
					root.rootID == command.RootID &&
					isStrictNewReportPair(ex.commands, commandIndex) {
					newReports = append(newReports, commandCandidate{exchangeIndex, command, target.name})
				}

				if targetOK && rootOK &&
					strings.EqualFold(target.name, "SaveAndClose") &&
					target.rootID == command.RootID &&
					strings.EqualFold(root.name, "ExpenseReportDetails_form") &&
					root.rootID == command.RootID {
					saves = append(saves, commandCandidate{exchangeIndex, command, target.name})
				}
			}

			for i, setValue := range ex.commands {
				if !strings.EqualFold(setValue.CommandName, "SetValue") || !setValueSucceeded(ex, setValue) {
					continue
				}
				purpose, purposeOK := catalog.lookup(setValue.TargetID)
				dialog, dialogOK := catalog.lookup(setValue.RootID)
				if !purposeOK || !dialogOK ||
					!strings.EqualFold(purpose.name, "NamePurpose") ||
					purpose.rootID != setValue.RootID ||
					!strings.EqualFold(purpose.rootName, "ExpenseNewExpenseReport_form") ||
					!strings.EqualFold(dialog.name, "ExpenseNewExpenseReport_form") ||
					dialog.rootID != setValue.RootID {
					continue
				}

				for j := i + 1; j < len(ex.commands); j++ {
					invoke := ex.commands[j]
					if !strings.EqualFold(invoke.CommandName, "ExecuteShortcuts") ||
						!hasOnlyPositionalString(invoke, "InvokeDefaultButton") ||
						invoke.RootID != setValue.RootID ||
						invoke.TargetID != invoke.RootID {
						continue
					}
					creates = append(creates, createCandidate{
						exchangeIndex:     exchangeIndex,
						setValue:          setValue,
						setControlName:    purpose.name,
						invokeDefault:     invoke,
						invokeControlName: dialog.name,
					})
					break
				}
			}
		}

	}

	if len(newReports) == 0 {
		return DraftFlow{}, errors.New("captured draft flow is missing the workspace new-report request")
	}
	if len(creates) == 0 {
		return DraftFlow{}, errors.New("captured draft flow is missing a successful SetValue+InvokeDefaultButton request against NamePurpose in ExpenseNewExpenseReport_form")
	}
	if len(saves) == 0 {
		return DraftFlow{}, errors.New("captured draft flow is missing the SaveAndClose request in ExpenseReportDetails_form")
	}

	var matched *DraftFlow
	for _, newReport := range newReports {
		for _, create := range creates {
			if create.exchangeIndex <= newReport.exchangeIndex {
				continue
			}
			for _, save := range saves {
				if save.exchangeIndex <= create.exchangeIndex {
					continue
				}
				flow := DraftFlow{
					NewReport: commandTarget(newReport.command, newReport.controlName),
					CreateDraft: CreateDraftRequest{
						SetValue:            commandTarget(create.setValue, create.setControlName),
						InvokeDefaultButton: commandTarget(create.invokeDefault, create.invokeControlName),
					},
					SaveAndClose: commandTarget(save.command, save.controlName),
				}
				if matched != nil {
					return DraftFlow{}, errors.New("captured draft flow is ambiguous; multiple complete new-report/create/save flows were observed")
				}
				matched = &flow
			}
		}
	}
	if matched == nil {
		return DraftFlow{}, errors.New("captured draft requests were not observed in new-report, create, save order")
	}
	return *matched, nil
}

func exchangeSucceeded(ex *exchange) bool {
	if ex == nil || ex.status < 200 || ex.status >= 300 || ex.response == nil || ex.response.LastAcknowledgedSequenceNumber == nil {
		return false
	}
	return *ex.response.LastAcknowledgedSequenceNumber >= ex.maxRequestSequence
}

func setValueSucceeded(ex *exchange, command observedCommand) bool {
	if !exchangeSucceeded(ex) {
		return false
	}
	if command.CallbackID != "" && !ex.callbackIDs[command.CallbackID] {
		return false
	}
	if command.FailureCallbackID != "" && ex.callbackIDs[command.FailureCallbackID] {
		return false
	}
	return true
}

func isStrictNewReportPair(commands []observedCommand, clickIndex int) bool {
	if clickIndex <= 0 || clickIndex >= len(commands) {
		return false
	}
	click := commands[clickIndex]
	update := commands[clickIndex-1]
	return update.messageIndex == click.messageIndex &&
		update.interactionIndex+1 == click.interactionIndex &&
		strings.EqualFold(update.CommandName, "UpdateLastSelectedControl") &&
		update.RootID == click.RootID && update.TargetID == click.RootID &&
		hasOnlyPositionalString(update, "NewExpenseReportReportsTab")
}

func hasOnlyPositionalString(command observedCommand, wanted string) bool {
	if len(command.PositionalParameters) != 1 {
		return false
	}
	value, ok := positionalString(command, 0)
	return ok && strings.EqualFold(value, wanted)
}

func positionalString(command observedCommand, index int) (string, bool) {
	if index < 0 || index >= len(command.PositionalParameters) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(command.PositionalParameters[index], &value); err != nil {
		return "", false
	}
	return value, value != ""
}

func commandTarget(command observedCommand, controlName string) CommandTarget {
	return CommandTarget{
		CommandName: command.CommandName,
		RootID:      command.RootID,
		TargetID:    command.TargetID,
		ControlName: controlName,
	}
}

func normalizeControlName(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func newTargetCatalog() *targetCatalog {
	return &targetCatalog{
		models:    make(map[string]targetModel),
		ambiguous: make(map[string]bool),
	}
}

func (catalog *targetCatalog) lookup(id string) (targetModel, bool) {
	if catalog == nil || id == "" || catalog.ambiguous[id] {
		return targetModel{}, false
	}
	model, ok := catalog.models[id]
	return model, ok
}

func (catalog *targetCatalog) collect(body []byte) {
	if catalog == nil || len(bytes.TrimSpace(body)) == 0 {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return
	}
	catalog.walk(value, "", "")
}

func (catalog *targetCatalog) walk(value any, rootID, rootName string) {
	switch value := value.(type) {
	case map[string]any:
		if explicitRoot, ok := value["RootId"].(string); ok && explicitRoot != "" {
			rootID = explicitRoot
			if root, ok := catalog.lookup(explicitRoot); ok && strings.HasSuffix(strings.ToLower(root.name), "_form") {
				rootName = root.name
			}
		}
		id, _ := value["Id"].(string)
		name, _ := value["Name"].(string)
		if id != "" && name != "" && strings.HasSuffix(strings.ToLower(name), "_form") {
			rootID = id
			rootName = name
		}
		if id != "" && name != "" {
			catalog.observe(id, targetModel{name: name, rootID: rootID, rootName: rootName})
		}

		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			catalog.walk(value[key], rootID, rootName)
		}
	case []any:
		for _, child := range value {
			catalog.walk(child, rootID, rootName)
		}
	}
}

func (catalog *targetCatalog) observe(id string, model targetModel) {
	if catalog.ambiguous[id] {
		return
	}
	if previous, ok := catalog.models[id]; ok {
		if previous.rootID != model.rootID ||
			!strings.EqualFold(previous.name, model.name) ||
			!strings.EqualFold(previous.rootName, model.rootName) {
			delete(catalog.models, id)
			catalog.ambiguous[id] = true
		}
		return
	}
	catalog.models[id] = model
}

func mergeReplayHeaders(destination http.Header, headers []harNameValue) {
	for _, header := range headers {
		if !isReplayHeader(header.Name) {
			continue
		}
		destination.Set(header.Name, header.Value)
	}
}

func isReplayHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "accept", "accept-language", "authorization", "content-type", "origin", "referer", "user-agent", "x-requested-with":
		return true
	default:
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "ms-dyn-")
	}
}

func mergeCookies(destination map[string]*http.Cookie, harCookies []harCookie, headers []harNameValue) {
	for _, cookie := range harCookies {
		converted := convertCookie(cookie)
		if converted.Name != "" {
			destination[cookieKey(converted)] = converted
		}
	}

	for _, header := range headers {
		if !strings.EqualFold(header.Name, "Cookie") || strings.TrimSpace(header.Value) == "" {
			continue
		}
		request := &http.Request{Header: http.Header{"Cookie": []string{header.Value}}}
		for _, cookie := range request.Cookies() {
			if cookieNameExists(destination, cookie.Name) {
				continue
			}
			destination[cookieKey(cookie)] = cookie
		}
	}
}

func cookieNameExists(cookies map[string]*http.Cookie, name string) bool {
	for _, cookie := range cookies {
		if cookie != nil && strings.EqualFold(cookie.Name, name) {
			return true
		}
	}
	return false
}

func convertCookie(cookie harCookie) *http.Cookie {
	converted := &http.Cookie{
		Name:     cookie.Name,
		Value:    cookie.Value,
		Path:     cookie.Path,
		Domain:   cookie.Domain,
		HttpOnly: cookie.HTTPOnly,
		Secure:   cookie.Secure,
	}
	if cookie.Expires != "" {
		if expires, err := time.Parse(time.RFC3339, cookie.Expires); err == nil {
			converted.Expires = expires
		} else if expires, err := http.ParseTime(cookie.Expires); err == nil {
			converted.Expires = expires
		}
	}
	switch strings.ToLower(cookie.SameSite) {
	case "strict":
		converted.SameSite = http.SameSiteStrictMode
	case "lax":
		converted.SameSite = http.SameSiteLaxMode
	case "none":
		converted.SameSite = http.SameSiteNoneMode
	}
	return converted
}

func cookieKey(cookie *http.Cookie) string {
	return strings.ToLower(cookie.Name) + "\x00" + cookie.Domain + "\x00" + cookie.Path
}

func sortedCookies(cookies map[string]*http.Cookie) []*http.Cookie {
	result := make([]*http.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		clone := *cookie
		result = append(result, &clone)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		if result[i].Domain != result[j].Domain {
			return result[i].Domain < result[j].Domain
		}
		return result[i].Path < result[j].Path
	})
	return result
}

func trimJSONBOM(data []byte) []byte {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	data = bytes.TrimPrefix(data, []byte("ï»¿"))
	return data
}

func decodeHARContent(text, encoding string) ([]byte, error) {
	if encoding == "" {
		return []byte(text), nil
	}
	if !strings.EqualFold(encoding, "base64") {
		return nil, fmt.Errorf("unsupported HAR content encoding %q", encoding)
	}
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	return decoded, nil
}
