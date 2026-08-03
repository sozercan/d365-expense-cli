package dynamics_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func TestDiscoverAndValidateSubmitButton(t *testing.T) {
	button := submitButtonNode(t)
	if button.ID != "submit-id" || button.Name != dynamics.ControlSubmitButton || button.TypeName != "Button" || button.RootID != "details-root" {
		t.Fatalf("SubmitButton = %#v", button)
	}
	if _, ok := button.Commands[dynamics.CommandClick]; !ok {
		t.Fatal("SubmitButton Click descriptor was not retained")
	}
	if err := dynamics.ValidateSubmitButton(button, "details-root"); err != nil {
		t.Fatalf("ValidateSubmitButton() error = %v", err)
	}
}

func TestValidateSubmitButtonDoesNotRequireEnglishLabel(t *testing.T) {
	tests := map[string]func(*dynamics.ModelNode){
		"localized": func(node *dynamics.ModelNode) {
			node.ValueProperties["Label"] = submitRaw(t, "Einreichen")
		},
		"missing": func(node *dynamics.ModelNode) {
			delete(node.ValueProperties, "Label")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			node := submitButtonNode(t)
			mutate(&node)
			if err := dynamics.ValidateSubmitButton(node, "details-root"); err != nil {
				t.Fatalf("ValidateSubmitButton() error = %v", err)
			}
		})
	}

	node := submitButtonNode(t)
	node.ValueProperties["MenuItemName"] = submitRaw(t, "TrvSubmitWorkflow")
	if err := dynamics.ValidateSubmitButton(node, "details-root"); err == nil {
		t.Fatal("ValidateSubmitButton() accepted changed stable menu-item metadata")
	}
}

func TestValidateSubmitButtonRejectsMetadataDrift(t *testing.T) {
	tests := map[string]func(*testing.T, *dynamics.ModelNode, *string){
		"empty details root": func(_ *testing.T, _ *dynamics.ModelNode, root *string) { *root = "" },
		"empty ID":           func(_ *testing.T, node *dynamics.ModelNode, _ *string) { node.ID = "" },
		"wrong name":         func(_ *testing.T, node *dynamics.ModelNode, _ *string) { node.Name = "SubmitWorkflowButton" },
		"wrong type":         func(_ *testing.T, node *dynamics.ModelNode, _ *string) { node.TypeName = "CommandButton" },
		"wrong root":         func(_ *testing.T, node *dynamics.ModelNode, _ *string) { node.RootID = "workspace" },
		"wrong menu item": func(t *testing.T, node *dynamics.ModelNode, _ *string) {
			node.ValueProperties["MenuItemName"] = submitRaw(t, "TrvSubmitWorkflow")
		},
		"disabled": func(t *testing.T, node *dynamics.ModelNode, _ *string) {
			node.SerializedValueProperties["Enabled"] = submitRaw(t, "false")
		},
		"not visible": func(t *testing.T, node *dynamics.ModelNode, _ *string) {
			node.SerializedValueProperties["Visible"] = submitRaw(t, "false")
		},
		"missing Click": func(_ *testing.T, node *dynamics.ModelNode, _ *string) {
			delete(node.Commands, dynamics.CommandClick)
		},
		"wrong Click value type": func(t *testing.T, node *dynamics.ModelNode, _ *string) {
			mutateSubmitClick(t, node, func(command map[string]any) { command["ValueTypeName"] = "InteractionCommand" })
		},
		"nonempty bindings": func(t *testing.T, node *dynamics.ModelNode, _ *string) {
			mutateSubmitClick(t, node, func(command map[string]any) { command["ParameterBindings"] = map[string]any{"x": 1} })
		},
		"nonblocking Click": func(t *testing.T, node *dynamics.ModelNode, _ *string) {
			mutateSubmitClick(t, node, func(command map[string]any) {
				command["Properties"].(map[string]any)["ShouldBlockOnExecution"] = false
			})
		},
		"extra Click metadata": func(t *testing.T, node *dynamics.ModelNode, _ *string) {
			mutateSubmitClick(t, node, func(command map[string]any) { command["Future"] = true })
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			node := submitButtonNode(t)
			root := "details-root"
			mutate(t, &node, &root)
			if err := dynamics.ValidateSubmitButton(node, root); err == nil {
				t.Fatal("ValidateSubmitButton() unexpectedly succeeded")
			}
		})
	}
}

func TestBuildSubmitClickMessageMatchesInferredShape(t *testing.T) {
	message := dynamics.BuildSubmitClickMessage(42, "details-root", "submit-id")
	if message.SequenceNumber != 42 || len(message.Interactions) != 1 {
		t.Fatalf("message = %#v", message)
	}
	command := mustCommands(t, message)[0]
	if command.CommandName != dynamics.CommandClick || command.RootID != "details-root" || command.TargetID != "submit-id" ||
		command.PositionalParameters != nil || command.NoAsyncIncrement || !command.Telemetry || !command.Throttle ||
		command.ThrottleID != "details-root_TG" || command.ShouldBlockOnExecution == nil || !*command.ShouldBlockOnExecution {
		t.Fatalf("Submit Click = %#v", command)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(message.Interactions[0], &object); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(object["PositionalParameters"]), []byte("null")) {
		t.Fatalf("PositionalParameters = %s, want null", object["PositionalParameters"])
	}
}

func TestValidateSubmitCommandsExactAllowlist(t *testing.T) {
	targets := dynamics.SubmitCommandTargets{DetailsRootID: "details-root", SubmitButtonID: "submit-id"}
	valid := func() dynamics.Envelope {
		return dynamics.Envelope{Messages: []dynamics.Message{
			dynamics.BuildSubmitClickMessage(42, targets.DetailsRootID, targets.SubmitButtonID),
		}}
	}
	if err := dynamics.ValidateSubmitCommands(valid(), targets); err != nil {
		t.Fatalf("ValidateSubmitCommands() error = %v", err)
	}

	tests := map[string]func() (dynamics.Envelope, dynamics.SubmitCommandTargets){
		"empty targets": func() (dynamics.Envelope, dynamics.SubmitCommandTargets) {
			return valid(), dynamics.SubmitCommandTargets{}
		},
		"wrong target": func() (dynamics.Envelope, dynamics.SubmitCommandTargets) {
			wrong := targets
			wrong.SubmitButtonID = "save"
			return valid(), wrong
		},
		"two messages": func() (dynamics.Envelope, dynamics.SubmitCommandTargets) {
			envelope := valid()
			envelope.Messages = append(envelope.Messages, dynamics.BuildSubmitClickMessage(43, targets.DetailsRootID, targets.SubmitButtonID))
			return envelope, targets
		},
		"two interactions": func() (dynamics.Envelope, dynamics.SubmitCommandTargets) {
			envelope := valid()
			envelope.Messages[0].Interactions = append(envelope.Messages[0].Interactions, envelope.Messages[0].Interactions[0])
			return envelope, targets
		},
		"zero sequence": func() (dynamics.Envelope, dynamics.SubmitCommandTargets) {
			envelope := valid()
			envelope.Messages[0].SequenceNumber = 0
			return envelope, targets
		},
		"empty positional parameters": func() (dynamics.Envelope, dynamics.SubmitCommandTargets) {
			envelope := valid()
			command := mustCommands(t, envelope.Messages[0])[0]
			command.PositionalParameters = []any{}
			envelope.Messages[0].Interactions[0] = mustRawCommand(t, command)
			return envelope, targets
		},
		"generic workflow submit": func() (dynamics.Envelope, dynamics.SubmitCommandTargets) {
			envelope := valid()
			command := mustCommands(t, envelope.Messages[0])[0]
			command.CommandName = "SubmitToWorkflow"
			envelope.Messages[0].Interactions[0] = mustRawCommand(t, command)
			return envelope, targets
		},
		"extra command field": func() (dynamics.Envelope, dynamics.SubmitCommandTargets) {
			envelope := valid()
			command := mustCommands(t, envelope.Messages[0])[0]
			command.ExtraFields = map[string]json.RawMessage{"Future": json.RawMessage("true")}
			envelope.Messages[0].Interactions[0] = mustRawCommand(t, command)
			return envelope, targets
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			envelope, allowlist := build()
			if err := dynamics.ValidateSubmitCommands(envelope, allowlist); !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}

func TestReceiptModelSurfacesAndMergesSubmitButton(t *testing.T) {
	fixture := []byte(`{"Descriptor":{"Id":"details-root","Name":"ExpenseReportDetails_form","TypeName":"Form","ChildViewModels":[{"Id":"submit-id","Name":"SubmitButton","TypeName":"Button"}]}}`)
	model, err := dynamics.DiscoverReceiptModel(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if model.SubmitButton.ID != "submit-id" || model.SubmitButton.RootID != "details-root" {
		t.Fatalf("SubmitButton = %#v", model.SubmitButton)
	}
	if model.CommandTargets().DetailsRootID != "details-root" || !strings.Contains(model.SafeSummary(), "submit=true") {
		t.Fatalf("receipt model did not surface SubmitButton: %s", model.SafeSummary())
	}
	merged := dynamics.MergeReceiptModels(
		dynamics.ReceiptModel{SubmitButton: dynamics.ModelNode{ID: "old"}},
		dynamics.ReceiptModel{SubmitButton: model.SubmitButton},
	)
	if merged.SubmitButton.ID != "submit-id" {
		t.Fatalf("merged SubmitButton = %#v", merged.SubmitButton)
	}
}

func submitButtonNode(t *testing.T) dynamics.ModelNode {
	t.Helper()
	fixture := map[string]any{"Descriptor": map[string]any{
		"Id": "details-root", "Name": dynamics.FormExpenseReportDetails, "TypeName": "Form",
		"ChildViewModels": []any{map[string]any{
			"Id": "submit-id", "Name": dynamics.ControlSubmitButton, "TypeName": "Button",
			"ValueProperties": map[string]any{
				"Label": "Submit", "MenuItemName": dynamics.MenuItemSubmit, "MenuItemType": "Action",
				"PrimaryModelName": "TrvExpTable_ds", "ServiceBoundary": "TrvExpTable",
			},
			"SerializedValueProperties": map[string]any{"Enabled": "true", "Visible": "true", "SaveRecord": "true"},
			"Commands": map[string]any{dynamics.CommandClick: map[string]any{
				"CommandName": dynamics.CommandClick, "ParameterBindings": map[string]any{}, "ValueTypeName": "Navigate",
				"Properties": map[string]any{
					"ExecuteImmediate": true, "ShouldBlockOnExecution": true, "Telemetry": "true", "ThrottleGroup": "TG",
				},
			}},
		}},
	}}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	model, err := dynamics.DiscoverResponseModel(data)
	if err != nil {
		t.Fatal(err)
	}
	button, ok := model.FindControl(dynamics.ControlSubmitButton)
	if !ok {
		t.Fatal("SubmitButton not discovered")
	}
	return button
}

func submitRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mutateSubmitClick(t *testing.T, node *dynamics.ModelNode, mutate func(map[string]any)) {
	t.Helper()
	var command map[string]any
	if err := json.Unmarshal(node.Commands[dynamics.CommandClick], &command); err != nil {
		t.Fatal(err)
	}
	mutate(command)
	node.Commands[dynamics.CommandClick] = submitRaw(t, command)
}
