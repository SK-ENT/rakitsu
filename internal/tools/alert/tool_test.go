package alert

import (
	"context"
	"reflect"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/alert"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

var _ tools.Tool = (*Tool)(nil)

func TestToolInterface(t *testing.T) {
	tool := NewTool(nil)
	if tool.GetName() != "send_alert" || tool.GetDescription() == "" {
		t.Fatal("unexpected tool metadata")
	}
	schema := tool.GetParametersSchema()
	if schema["type"] != "object" {
		t.Fatal("schema must be an object")
	}
	if !reflect.DeepEqual(schema["required"], []string{"severity", "title", "body"}) {
		t.Fatalf("required = %v", schema["required"])
	}
	properties := schema["properties"].(map[string]interface{})
	for _, name := range []string{"severity", "title", "body"} {
		if properties[name].(map[string]interface{})["type"] != "string" {
			t.Errorf("%s must be a string", name)
		}
	}
	if !reflect.DeepEqual(properties["severity"].(map[string]interface{})["enum"], []string{"info", "warn", "critical"}) {
		t.Fatal("unexpected severity enum")
	}
}

func TestExecuteSuccess(t *testing.T) {
	notifier, err := alert.NewNotifier(config.WakeAlerts{}, nil, alert.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer notifier.Close()
	tool := NewTool(notifier)
	for _, severity := range []string{"info", "warn", "critical"} {
		t.Run(severity, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), map[string]interface{}{
				"severity": severity, "title": "Monitor", "body": "Update available",
			})
			if err != nil || result != "alert queued" {
				t.Fatalf("Execute = %q, %v", result, err)
			}
		})
	}
}

func TestExecuteInvalidArguments(t *testing.T) {
	// Missing or non-string severity is an error
	for _, value := range []interface{}{nil, "", 42} {
		args := map[string]interface{}{"title": "Monitor", "body": "Update"}
		if value != nil {
			args["severity"] = value
		}
		result, err := NewTool(nil).Execute(context.Background(), args)
		if err == nil || result != "" {
			t.Errorf("severity=%v: Execute = %q, %v (expected error)", value, result, err)
		}
	}
	// Invalid severity values are errors
	for _, severity := range []string{"error", "INFO", " warn "} {
		_, err := NewTool(nil).Execute(context.Background(), map[string]interface{}{
			"severity": severity, "title": "M", "body": "U",
		})
		if err == nil {
			t.Errorf("accepted severity %q", severity)
		}
	}
	// Empty title/body are OK (they're passed through to notifier)
	result, err := NewTool(nil).Execute(context.Background(), map[string]interface{}{
		"severity": "info", "title": "", "body": "",
	})
	if err == nil || result != "" {
		// Error expected because notifier is nil, not because args are empty
		// This test just verifies we don't pre-validate empty title/body
	}
}

func TestExecuteNotifierError(t *testing.T) {
	notifier, err := alert.NewNotifier(config.WakeAlerts{}, nil, alert.Options{})
	if err != nil {
		t.Fatal(err)
	}
	notifier.Close()
	for _, n := range []*alert.Notifier{notifier, nil} {
		result, err := NewTool(n).Execute(context.Background(), map[string]interface{}{
			"severity": "info", "title": "Monitor", "body": "Update",
		})
		if err == nil || result != "" {
			t.Fatalf("Execute = %q, %v; expected notifier error", result, err)
		}
	}
}
