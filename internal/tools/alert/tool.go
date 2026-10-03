// Package alert provides a tool for queueing agent alerts.
package alert

import (
	"context"
	"fmt"

	"github.com/SK-ENT/rakitsu/internal/alert"
)

// Tool wraps a notifier. The caller owns the notifier's lifecycle.
type Tool struct {
	notifier *alert.Notifier
}

func NewTool(notifier *alert.Notifier) *Tool {
	return &Tool{notifier: notifier}
}

func (t *Tool) GetName() string { return "send_alert" }

func (t *Tool) GetDescription() string {
	return "Queue an alert with a severity, title, and body."
}

func (t *Tool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"severity": map[string]interface{}{
				"type": "string",
				"enum": []string{"info", "warn", "critical"},
			},
			"title": map[string]interface{}{"type": "string"},
			"body":  map[string]interface{}{"type": "string"},
		},
		"required": []string{"severity", "title", "body"},
	}
}

func (t *Tool) Execute(_ context.Context, args map[string]interface{}) (string, error) {
	severity, ok := args["severity"].(string)
	if !ok || severity == "" {
		return "", fmt.Errorf("severity is required and must be info, warn, or critical")
	}
	switch severity {
	case "info", "warn", "critical":
	default:
		return "", fmt.Errorf("severity must be info, warn, or critical")
	}
	title, _ := args["title"].(string)
	body, _ := args["body"].(string)
	if t.notifier == nil {
		return "", fmt.Errorf("alert notifier is unavailable")
	}
	if err := t.notifier.Send(severity, title, body); err != nil {
		return "", err
	}
	return "alert queued", nil
}
