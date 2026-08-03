package dynamics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ModelNode is a named model or view-model descriptor found recursively in a
// server response. RootID is the nearest enclosing form/view-model root; for a
// form node it is the node's own ID.
type ModelNode struct {
	ID                        string
	Name                      string
	TypeName                  string
	RootID                    string
	Properties                map[string]json.RawMessage
	ValueProperties           map[string]json.RawMessage
	SerializedValueProperties map[string]json.RawMessage
	Commands                  map[string]json.RawMessage
	Path                      []string
	Raw                       json.RawMessage
}

// ResponseModel is the useful subset discovered from a Dynamics response.
// Forms and Controls are keyed by their exact Name values.
type ResponseModel struct {
	Forms        map[string]ModelNode
	Controls     map[string]ModelNode
	ReportNumber string
	Status       string

	controlsByRoot map[string]map[string]ModelNode
	formsByName    map[string]map[string]ModelNode
}

// DiscoverResponseModel recursively searches any JSON response value. It does
// not depend on concrete server interaction types, which makes it tolerant of
// newly added interaction and descriptor fields.
func DiscoverResponseModel(data []byte) (ResponseModel, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return ResponseModel{}, fmt.Errorf("discover Dynamics response model: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ResponseModel{}, fmt.Errorf("discover Dynamics response model: multiple JSON values")
		}
		return ResponseModel{}, fmt.Errorf("discover Dynamics response model: trailing JSON: %w", err)
	}

	state := discoveryState{
		model: ResponseModel{
			Forms:          make(map[string]ModelNode),
			Controls:       make(map[string]ModelNode),
			controlsByRoot: make(map[string]map[string]ModelNode),
			formsByName:    make(map[string]map[string]ModelNode),
		},
		formScores:        make(map[string]int),
		formIDScores:      make(map[string]map[string]int),
		controlScores:     make(map[string]int),
		controlRootScores: make(map[string]map[string]int),
	}
	state.walk(value, nil, "", "")
	state.preferControlInForm(ControlSubmitButton, FormExpenseReportDetails)
	state.applyDetailsRecord()
	return state.model, nil
}

// DiscoverEnvelopeModel discovers response models from an already parsed
// envelope.
func DiscoverEnvelopeModel(envelope Envelope) (ResponseModel, error) {
	data, err := MarshalEnvelope(envelope)
	if err != nil {
		return ResponseModel{}, fmt.Errorf("discover Dynamics envelope model: %w", err)
	}
	model, err := DiscoverResponseModel(data)
	if err != nil {
		return ResponseModel{}, fmt.Errorf("discover Dynamics envelope model: %w", err)
	}
	return model, nil
}

// FindForm looks up a recursively discovered form by exact Name.
func (model ResponseModel) FindForm(name string) (ModelNode, bool) {
	node, ok := model.Forms[name]
	return node, ok
}

// FindUniqueForm returns a form only when the response contains one distinct
// form ID for the exact Name. Repeated deltas for the same ID are allowed;
// competing roots are ambiguous and fail closed.
func (model ResponseModel) FindUniqueForm(name string) (ModelNode, bool) {
	forms := model.formsByName[name]
	if len(forms) == 1 {
		for _, form := range forms {
			return form, true
		}
	}
	if len(forms) > 1 {
		return ModelNode{}, false
	}
	return model.FindForm(name)
}

// FindControl looks up a recursively discovered control by exact Name.
func (model ResponseModel) FindControl(name string) (ModelNode, bool) {
	node, ok := model.Controls[name]
	return node, ok
}

// FindControlInRoot looks up a recursively discovered control by exact Name
// under the selected form/view-model root. Unlike FindControl, it can
// distinguish same-name controls that belong to different roots.
func (model ResponseModel) FindControlInRoot(name, rootID string) (ModelNode, bool) {
	if rootID == "" {
		return ModelNode{}, false
	}
	if controls := model.controlsByRoot[rootID]; controls != nil {
		if node, ok := controls[name]; ok {
			return node, true
		}
	}
	// Keep the method useful for ResponseModel values assembled directly by
	// callers rather than by DiscoverResponseModel.
	node, ok := model.Controls[name]
	return node, ok && node.RootID == rootID
}

// FindModel looks up either a form or a control by exact Name, preferring a
// form when both maps contain the name.
func (model ResponseModel) FindModel(name string) (ModelNode, bool) {
	if node, ok := model.FindForm(name); ok {
		return node, true
	}
	return model.FindControl(name)
}

type discoveryState struct {
	model             ResponseModel
	formScores        map[string]int
	formIDScores      map[string]map[string]int
	controlScores     map[string]int
	controlRootScores map[string]map[string]int
	report            propertyCandidate
	status            propertyCandidate
	detailsRecord     detailsRecordCandidate
	detailsFormReport string
}

type propertyCandidate struct {
	value string
	score int
}

type detailsRecordCandidate struct {
	report string
	status string
	score  int
	path   string
	found  bool
}

func (state *discoveryState) walk(value any, path []string, rootID, rootName string) {
	switch typed := value.(type) {
	case map[string]any:
		if rootID == "" {
			rootID = stringValue(typed["RootId"])
		}
		name := stringValue(typed["Name"])
		id := stringValue(typed["Id"])
		isForm := name != "" && isFormName(name)
		if isForm && id != "" {
			rootID = id
			rootName = name
		}

		if name != "" && id != "" {
			node := modelNodeFromObject(typed, name, id, rootID, path)
			score := modelObjectScore(typed, rootID != "")
			if isForm {
				node.RootID = id
				state.keepForm(node, score)
			} else {
				state.keepControl(node, score)
			}
		}
		if strings.EqualFold(name, FormExpenseReportDetails) {
			if valueProperties, ok := typed["ValueProperties"].(map[string]any); ok {
				if title, ok := scalarString(valueProperties["ParentTitleFields"]); ok {
					state.detailsFormReport = reportNumberFromTitleFields(title)
				}
			}
		}

		if properties, ok := typed["Properties"].(map[string]any); ok {
			state.inspectProperties(properties, rootName)
		}
		if rootName == FormExpenseReportDetails && isExpenseReportCollection(typed, path) {
			state.inspectDetailsCollection(typed, path)
		}

		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			state.walk(typed[key], appendPath(path, key), rootID, rootName)
		}
	case []any:
		for index, item := range typed {
			state.walk(item, appendPath(path, fmt.Sprintf("%d", index)), rootID, rootName)
		}
	}
}

func (state *discoveryState) keepNode(nodes map[string]ModelNode, scores map[string]int, node ModelNode, score int) {
	previousScore, exists := scores[node.Name]
	if exists && previousScore >= score {
		return
	}
	nodes[node.Name] = node
	scores[node.Name] = score
}

func (state *discoveryState) keepForm(node ModelNode, score int) {
	state.keepNode(state.model.Forms, state.formScores, node, score)
	if state.model.formsByName[node.Name] == nil {
		state.model.formsByName[node.Name] = make(map[string]ModelNode)
	}
	if state.formIDScores[node.Name] == nil {
		state.formIDScores[node.Name] = make(map[string]int)
	}
	previousScore, exists := state.formIDScores[node.Name][node.ID]
	if exists && previousScore >= score {
		return
	}
	state.model.formsByName[node.Name][node.ID] = node
	state.formIDScores[node.Name][node.ID] = score
}

func (state *discoveryState) keepControl(node ModelNode, score int) {
	state.keepNode(state.model.Controls, state.controlScores, node, score)
	if node.RootID == "" {
		return
	}
	if state.model.controlsByRoot[node.RootID] == nil {
		state.model.controlsByRoot[node.RootID] = make(map[string]ModelNode)
	}
	if state.controlRootScores[node.RootID] == nil {
		state.controlRootScores[node.RootID] = make(map[string]int)
	}
	state.keepNode(
		state.model.controlsByRoot[node.RootID],
		state.controlRootScores[node.RootID],
		node,
		score,
	)
}

func (state *discoveryState) preferControlInForm(controlName, formName string) {
	form, ok := state.model.FindForm(formName)
	if !ok {
		return
	}
	control, ok := state.model.FindControlInRoot(controlName, form.ID)
	if !ok {
		return
	}
	state.model.Controls[controlName] = control
}

func (state *discoveryState) inspectProperties(properties map[string]any, rootName string) {
	baseScore := 0
	if rootName == FormExpenseReportDetails {
		baseScore += 100
	}
	if stringValue(properties["dataSourceName_internal"]) == "TrvExpTable_ds" {
		baseScore += 10
	}

	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, ok := scalarString(properties[key])
		if !ok || value == "" {
			continue
		}
		normalized := normalizePropertyName(key)
		if score := reportPropertyScore(normalized); score > 0 {
			candidate := propertyCandidate{value: value, score: baseScore + score}
			if candidate.score > state.report.score {
				state.report = candidate
				state.model.ReportNumber = value
			}
		}
		if score := statusPropertyScore(normalized); score > 0 {
			candidate := propertyCandidate{value: value, score: baseScore + score}
			if candidate.score > state.status.score {
				state.status = candidate
				state.model.Status = value
			}
		}
	}
}

func (state *discoveryState) inspectDetailsCollection(collection map[string]any, path []string) {
	record, selectionScore, ok := selectActiveRecord(collection)
	if !ok {
		return
	}
	report, status := discoverRecordValues(record)
	candidate := detailsRecordCandidate{
		report: report,
		status: status,
		score:  selectionScore*1000 - len(path),
		path:   strings.Join(path, "\x00"),
		found:  true,
	}
	if state.detailsRecord.found && (candidate.score < state.detailsRecord.score ||
		(candidate.score == state.detailsRecord.score && candidate.path >= state.detailsRecord.path)) {
		return
	}
	state.detailsRecord = candidate
}

func (state *discoveryState) applyDetailsRecord() {
	if !state.detailsRecord.found {
		if state.detailsFormReport != "" {
			state.model.ReportNumber = state.detailsFormReport
		}
		return
	}
	// Keep report number and status coupled to the same selected details-form
	// record. The details form's ParentTitleFields is a form-local fallback for
	// environments that do not serialize the report number on the active row.
	if state.detailsRecord.report != "" {
		state.model.ReportNumber = state.detailsRecord.report
	} else {
		state.model.ReportNumber = state.detailsFormReport
	}
	state.model.Status = state.detailsRecord.status
}

func reportNumberFromTitleFields(value string) string {
	parts := strings.Split(value, ":")
	for index := len(parts) - 1; index >= 0; index-- {
		candidate := strings.TrimSpace(parts[index])
		if plausibleReportNumber(candidate) {
			return candidate
		}
	}
	return ""
}

func plausibleReportNumber(value string) bool {
	if len(value) < 5 || len(value) > 64 {
		return false
	}
	hasDigit := false
	for _, character := range value {
		switch {
		case unicode.IsDigit(character):
			hasDigit = true
		case unicode.IsLetter(character), character == '-', character == '_', character == '/':
		default:
			return false
		}
	}
	return hasDigit
}

func isExpenseReportCollection(collection map[string]any, path []string) bool {
	if stringValue(collection["Name"]) == "TrvExpTable_ds" {
		return true
	}
	return len(path) != 0 && path[len(path)-1] == "TrvExpTable_ds"
}

func selectActiveRecord(collection map[string]any) (map[string]any, int, bool) {
	if record, ok := activeRecordModel(collection["ActiveRecordModel"]); ok {
		return record, 400, true
	}

	items, ok := collection["Items"].([]any)
	if !ok || len(items) == 0 {
		return nil, 0, false
	}
	records := make([]map[string]any, len(items))
	wrappers := make([]map[string]any, len(items))
	for index, item := range items {
		wrapper, _ := item.(map[string]any)
		wrappers[index] = wrapper
		records[index] = itemRecord(wrapper)
	}

	if activeID, ok := scalarString(collection["ActiveMasterTrackingId"]); ok && activeID != "" && activeID != "0" {
		activeID = normalizeTrackingID(activeID)
		for index, record := range records {
			if record != nil && recordMatchesTrackingID(wrappers[index], record, activeID) {
				return record, 300, true
			}
		}
	}

	for _, record := range records {
		if record == nil {
			continue
		}
		properties, _ := record["Properties"].(map[string]any)
		if marked, ok := scalarString(properties["_RESERVED_IsMarked"]); ok && strings.EqualFold(marked, "true") {
			return record, 250, true
		}
	}

	if index, ok := integerValue(collection["ActiveRecordIndex"]); ok && index >= 0 && index < len(records) && records[index] != nil {
		return records[index], 200, true
	}
	if len(records) == 1 && records[0] != nil {
		return records[0], 100, true
	}
	return nil, 0, false
}

func activeRecordModel(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if record := itemRecord(typed); record != nil {
			if report, status := discoverRecordValues(record); report != "" || status != "" {
				return record, true
			}
		}
	case []any:
		for _, item := range typed {
			if wrapper, ok := item.(map[string]any); ok {
				if record := itemRecord(wrapper); record != nil {
					if report, status := discoverRecordValues(record); report != "" || status != "" {
						return record, true
					}
				}
			}
		}
	}
	return nil, false
}

func itemRecord(wrapper map[string]any) map[string]any {
	if wrapper == nil {
		return nil
	}
	if record, ok := wrapper["Item"].(map[string]any); ok {
		return record
	}
	return wrapper
}

func recordMatchesTrackingID(wrapper, record map[string]any, activeID string) bool {
	properties, _ := record["Properties"].(map[string]any)
	for _, value := range []any{properties["MasterTrackingId"], wrapper["MasterTrackingId"], wrapper["Id"], record["Id"]} {
		if text, ok := scalarString(value); ok && normalizeTrackingID(text) == activeID {
			return true
		}
	}
	return false
}

func normalizeTrackingID(value string) string {
	return strings.TrimSuffix(strings.TrimSpace(value), "_0")
}

func integerValue(value any) (int, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.Atoi(typed.String())
		return parsed, err == nil
	case float64:
		return int(typed), typed == float64(int(typed))
	case int:
		return typed, true
	default:
		return 0, false
	}
}

func discoverRecordValues(record map[string]any) (string, string) {
	var report, status propertyCandidate
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if properties, ok := typed["Properties"].(map[string]any); ok {
				baseScore := 0
				if stringValue(properties["dataSourceName_internal"]) == "TrvExpTable_ds" {
					baseScore = 100
				}
				keys := make([]string, 0, len(properties))
				for key := range properties {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					text, ok := scalarString(properties[key])
					if !ok || text == "" {
						continue
					}
					normalized := normalizePropertyName(key)
					if score := reportPropertyScore(normalized); score > 0 && baseScore+score > report.score {
						report = propertyCandidate{value: text, score: baseScore + score}
					}
					if score := statusPropertyScore(normalized); score > 0 && baseScore+score > status.score {
						status = propertyCandidate{value: text, score: baseScore + score}
					}
				}
			}
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(typed[key])
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(record)
	return report.value, status.value
}

func modelNodeFromObject(object map[string]any, name, id, rootID string, path []string) ModelNode {
	node := ModelNode{
		ID:       id,
		Name:     name,
		TypeName: stringValue(object["TypeName"]),
		RootID:   rootID,
		Path:     append([]string(nil), path...),
	}
	if properties, ok := object["Properties"].(map[string]any); ok {
		node.Properties = rawObject(properties)
	}
	if properties, ok := object["ValueProperties"].(map[string]any); ok {
		node.ValueProperties = rawObject(properties)
	}
	if properties, ok := object["SerializedValueProperties"].(map[string]any); ok {
		node.SerializedValueProperties = rawObject(properties)
	}
	if commands, ok := object["Commands"].(map[string]any); ok {
		node.Commands = rawObject(commands)
	}
	if raw, err := json.Marshal(object); err == nil {
		node.Raw = raw
	}
	return node
}

func modelObjectScore(object map[string]any, hasRoot bool) int {
	score := 0
	if hasRoot {
		score += 20
	}
	if stringValue(object["TypeName"]) != "" {
		score += 50
	}
	if _, ok := object["ChildViewModels"].([]any); ok {
		score += 30
	}
	if _, ok := object["Commands"].(map[string]any); ok {
		score += 10
	}
	if _, ok := object["ChildModelCollections"].(map[string]any); ok {
		score += 5
	}
	return score
}

func rawObject(object map[string]any) map[string]json.RawMessage {
	if object == nil {
		return nil
	}
	raw := make(map[string]json.RawMessage, len(object))
	for key, value := range object {
		data, err := json.Marshal(value)
		if err == nil {
			raw[key] = data
		}
	}
	return raw
}

func isFormName(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), "_form")
}

func normalizePropertyName(name string) string {
	var builder strings.Builder
	builder.Grow(len(name))
	for _, character := range name {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(unicode.ToLower(character))
		}
	}
	return builder.String()
}

func reportPropertyScore(name string) int {
	switch name {
	case "expnumberfield":
		return 80
	case "expensereportnumberfield":
		return 75
	case "expensereportnumber":
		return 70
	case "reportnumberfield":
		return 65
	case "reportnumber":
		return 60
	default:
		return 0
	}
}

func statusPropertyScore(name string) int {
	switch name {
	case "expensereportstatusdatamethod":
		return 90
	case "expensereportstatus":
		return 80
	case "reportstatusdatamethod":
		return 75
	case "reportstatus":
		return 70
	case "approvalstatusfield":
		return 50
	default:
		return 0
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func scalarString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	case float64:
		return fmt.Sprint(typed), true
	case bool:
		return fmt.Sprint(typed), true
	default:
		return "", false
	}
}

func appendPath(path []string, segment string) []string {
	result := make([]string, len(path)+1)
	copy(result, path)
	result[len(path)] = segment
	return result
}
