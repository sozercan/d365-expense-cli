package expense

import (
	"encoding/json"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

// mergeSubmitButtonCandidate applies an incremental SubmitButton descriptor to
// the last complete candidate. Dynamics can rotate the control ID while
// omitting unchanged identity and command metadata from stage-local responses.
// Present fields replace prior values so the final strict validator still
// rejects genuine metadata drift, including explicit clears.
func mergeSubmitButtonCandidate(previous, update dynamics.ModelNode) dynamics.ModelNode {
	if previous.ID == "" {
		return update
	}
	fields := rawObjectFields(update.Raw)
	merged := previous
	if rawFieldPresent(fields, "Id") || update.ID != "" {
		merged.ID = update.ID
	}
	if rawFieldPresent(fields, "Name") || update.Name != "" {
		merged.Name = update.Name
	}
	if rawFieldPresent(fields, "TypeName") || update.TypeName != "" {
		merged.TypeName = update.TypeName
	}
	if update.RootID != "" {
		merged.RootID = update.RootID
	}
	merged.Properties = mergeRawProperties(previous.Properties, update.Properties, rawFieldPresent(fields, "Properties"))
	merged.ValueProperties = mergeRawProperties(previous.ValueProperties, update.ValueProperties, rawFieldPresent(fields, "ValueProperties"))
	merged.SerializedValueProperties = mergeRawProperties(previous.SerializedValueProperties, update.SerializedValueProperties, rawFieldPresent(fields, "SerializedValueProperties"))
	merged.Commands = mergeRawProperties(previous.Commands, update.Commands, rawFieldPresent(fields, "Commands"))
	if len(update.Path) != 0 {
		merged.Path = append([]string(nil), update.Path...)
	}
	if len(update.Raw) != 0 {
		merged.Raw = append(json.RawMessage(nil), update.Raw...)
	}
	return merged
}

func rawObjectFields(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	return fields
}

func rawFieldPresent(fields map[string]json.RawMessage, name string) bool {
	_, ok := fields[name]
	return ok
}

func mergeRawProperties(previous, update map[string]json.RawMessage, updatePresent bool) map[string]json.RawMessage {
	if updatePresent && len(update) == 0 {
		return nil
	}
	if len(previous) == 0 && len(update) == 0 {
		return nil
	}
	merged := make(map[string]json.RawMessage, len(previous)+len(update))
	for name, value := range previous {
		merged[name] = append(json.RawMessage(nil), value...)
	}
	for name, value := range update {
		merged[name] = append(json.RawMessage(nil), value...)
	}
	return merged
}
