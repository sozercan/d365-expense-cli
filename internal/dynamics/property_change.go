package dynamics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// InteractionTypePropertyChange is the observed type discriminator for a
	// client-side view-model property update.
	InteractionTypePropertyChange = "PropertyChangeInteraction"

	PropertyDocuName       = "DocuName"
	PropertyUploadedFileID = "UploadedFileId"
)

// PropertyChangeInteraction is the observed wire representation of an
// outbound Dynamics property update. NoAsyncIncrement is a pointer because the
// receipt flow distinguishes the omitted field on DocuName from true on
// UploadedFileId.
//
// Unknown JSON fields are retained for capture inspection and round trips, but
// outbound validators reject them. They also reject duplicate or case-variant
// spellings of reserved fields recorded while unmarshaling.
type PropertyChangeInteraction struct {
	Type                    string                     `json:"$type"`
	PropertyName            string                     `json:"PropertyName"`
	RootID                  string                     `json:"RootId"`
	TargetID                string                     `json:"TargetId"`
	NoAsyncIncrement        *bool                      `json:"NoAsyncIncrement,omitempty"`
	NewValue                string                     `json:"NewValue"`
	ExtraFields             map[string]json.RawMessage `json:"-"`
	reservedFieldConflicts  []string
	noAsyncIncrementPresent bool
}

var propertyChangeKnownFields = map[string]struct{}{
	"$type": {}, "PropertyName": {}, "RootId": {}, "TargetId": {},
	"NoAsyncIncrement": {}, "NewValue": {},
}

// ParsePropertyChangeInteraction parses a raw interaction as a property
// change. A non-empty $type other than PropertyChangeInteraction is rejected.
func ParsePropertyChangeInteraction(data json.RawMessage) (PropertyChangeInteraction, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return PropertyChangeInteraction{}, errors.New("parse Dynamics property change interaction: empty JSON")
	}
	var property PropertyChangeInteraction
	if err := json.Unmarshal(data, &property); err != nil {
		return PropertyChangeInteraction{}, fmt.Errorf("parse Dynamics property change interaction: %w", err)
	}
	if property.Type != "" && property.Type != InteractionTypePropertyChange {
		return PropertyChangeInteraction{}, fmt.Errorf(
			"parse Dynamics property change interaction: interaction type %q is not %q",
			property.Type,
			InteractionTypePropertyChange,
		)
	}
	return property, nil
}

// MarshalPropertyChangeInteraction marshals one property change, including
// retained unknown fields.
func MarshalPropertyChangeInteraction(property PropertyChangeInteraction) (json.RawMessage, error) {
	data, err := json.Marshal(property)
	if err != nil {
		return nil, fmt.Errorf("marshal Dynamics property change interaction: %w", err)
	}
	return json.RawMessage(data), nil
}

// ClonePropertyChangeInteraction returns a deep clone of a property change.
func ClonePropertyChangeInteraction(property PropertyChangeInteraction) PropertyChangeInteraction {
	clone := property
	if property.NoAsyncIncrement != nil {
		value := *property.NoAsyncIncrement
		clone.NoAsyncIncrement = &value
	}
	clone.ExtraFields = cloneRawMap(property.ExtraFields)
	clone.reservedFieldConflicts = append([]string(nil), property.reservedFieldConflicts...)
	return clone
}

func (property *PropertyChangeInteraction) UnmarshalJSON(data []byte) error {
	type propertyAlias PropertyChangeInteraction
	var value propertyAlias
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	extra, err := collectExtraFields(data, propertyChangeKnownFields)
	if err != nil {
		return err
	}
	conflicts, err := collectReservedFieldConflicts(data, propertyChangeKnownFields)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	*property = PropertyChangeInteraction(value)
	property.ExtraFields = extra
	property.reservedFieldConflicts = conflicts
	_, property.noAsyncIncrementPresent = object["NoAsyncIncrement"]
	return nil
}

func (property PropertyChangeInteraction) MarshalJSON() ([]byte, error) {
	type propertyAlias PropertyChangeInteraction
	return marshalWithExtraFields(propertyAlias(property), property.ExtraFields, propertyChangeKnownFields)
}

func propertyNoAsyncIncrementPresent(property PropertyChangeInteraction) bool {
	return property.NoAsyncIncrement != nil || property.noAsyncIncrementPresent
}
