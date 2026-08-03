package dynamics

import "encoding/json"

var modelNodeMapFields = []string{"Properties", "ValueProperties", "SerializedValueProperties", "Commands"}

// MergeModelNode applies an incremental descriptor to a previously discovered
// node. Fields omitted from the update are retained, while fields explicitly
// present (including empty/null values) replace prior state. This preserves
// rotated control IDs without hiding metadata drift from later validators.
func MergeModelNode(previous, update ModelNode) ModelNode {
	fields := modelNodePresentFields(update)
	resets := modelNodeResetFields(update, fields)
	merged := previous
	if modelNodeFieldPresent(fields, "Id") || update.ID != "" {
		merged.ID = update.ID
	}
	if modelNodeFieldPresent(fields, "Name") || update.Name != "" {
		merged.Name = update.Name
	}
	if modelNodeFieldPresent(fields, "TypeName") || update.TypeName != "" {
		merged.TypeName = update.TypeName
	}
	if update.RootID != "" {
		merged.RootID = update.RootID
	}
	merged.Properties = mergeModelNodeProperties(previous.Properties, update.Properties, modelNodeFieldPresent(fields, "Properties"), resets["Properties"])
	merged.ValueProperties = mergeModelNodeProperties(previous.ValueProperties, update.ValueProperties, modelNodeFieldPresent(fields, "ValueProperties"), resets["ValueProperties"])
	merged.SerializedValueProperties = mergeModelNodeProperties(previous.SerializedValueProperties, update.SerializedValueProperties, modelNodeFieldPresent(fields, "SerializedValueProperties"), resets["SerializedValueProperties"])
	merged.Commands = mergeModelNodeProperties(previous.Commands, update.Commands, modelNodeFieldPresent(fields, "Commands"), resets["Commands"])
	if len(update.Path) != 0 {
		merged.Path = append([]string(nil), update.Path...)
	}
	if len(update.Raw) != 0 {
		merged.Raw = append(json.RawMessage(nil), update.Raw...)
	}
	merged.presentFields = mergeModelNodeFlags(previous.presentFields, fields)
	merged.resetFields = mergeModelNodeFlags(previous.resetFields, resets)
	return merged
}

func modelNodePresentFields(node ModelNode) map[string]bool {
	fields := make(map[string]bool, len(node.presentFields))
	for name, present := range node.presentFields {
		if present {
			fields[name] = true
		}
	}
	if len(node.Raw) == 0 {
		return fields
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(node.Raw, &rawFields); err != nil {
		return fields
	}
	for name := range rawFields {
		fields[name] = true
	}
	return fields
}

func modelNodeResetFields(node ModelNode, present map[string]bool) map[string]bool {
	resets := make(map[string]bool, len(node.resetFields))
	for name, reset := range node.resetFields {
		if reset {
			resets[name] = true
		}
	}
	for _, name := range modelNodeMapFields {
		if !present[name] {
			continue
		}
		var values map[string]json.RawMessage
		switch name {
		case "Properties":
			values = node.Properties
		case "ValueProperties":
			values = node.ValueProperties
		case "SerializedValueProperties":
			values = node.SerializedValueProperties
		case "Commands":
			values = node.Commands
		}
		if len(values) == 0 {
			resets[name] = true
		}
	}
	return resets
}

func modelNodeFieldPresent(fields map[string]bool, name string) bool {
	return fields[name]
}

func mergeModelNodeFlags(previous, update map[string]bool) map[string]bool {
	if len(previous) == 0 && len(update) == 0 {
		return nil
	}
	merged := make(map[string]bool, len(previous)+len(update))
	for name, value := range previous {
		if value {
			merged[name] = true
		}
	}
	for name, value := range update {
		if value {
			merged[name] = true
		}
	}
	return merged
}

func mergeModelNodeProperties(previous, update map[string]json.RawMessage, updatePresent, reset bool) map[string]json.RawMessage {
	if reset && len(update) == 0 {
		return nil
	}
	if len(previous) == 0 && len(update) == 0 {
		return nil
	}
	capacity := len(update)
	if !reset {
		capacity += len(previous)
	}
	merged := make(map[string]json.RawMessage, capacity)
	if !reset {
		for name, value := range previous {
			merged[name] = append(json.RawMessage(nil), value...)
		}
	}
	if updatePresent || len(update) != 0 {
		for name, value := range update {
			merged[name] = append(json.RawMessage(nil), value...)
		}
	}
	return merged
}
