package wake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/SK-ENT/rakitsu/internal/tools"
)

// TaskHandle lets the agent build (which runs before the wake engine exists) get
// start_task / get_task_status tools that bind to the TaskManager later.
type TaskHandle struct {
	mu sync.RWMutex
	m  *TaskManager
}

type handleKey struct{}

func WithTaskHandle(ctx context.Context, h *TaskHandle) context.Context {
	return context.WithValue(ctx, handleKey{}, h)
}

func TaskHandleFrom(ctx context.Context) *TaskHandle {
	h, _ := ctx.Value(handleKey{}).(*TaskHandle)
	return h
}

func (h *TaskHandle) Bind(m *TaskManager) { h.mu.Lock(); h.m = m; h.mu.Unlock() }

func (h *TaskHandle) manager() (*TaskManager, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.m == nil {
		return nil, errors.New("wake tasks are not ready")
	}
	return h.m, nil
}

// Tools returns the two tools. Only wake sessions get them; started tasks never do.
func (h *TaskHandle) Tools() []tools.Tool { return []tools.Tool{&startTaskTool{h}, &taskStatusTool{h}} }

type startTaskTool struct{ h *TaskHandle }

func (t *startTaskTool) GetName() string { return "start_task" }
func (t *startTaskTool) GetDescription() string {
	return "Launch one allowlisted task config as an independent run. config_name must be an allowlisted name; arguments must contain exactly the declared parameters (enum, safe relative path, bounded integer). Anything else is refused. Returns a task_id; use get_task_status to check on it."
}
func (t *startTaskTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"config_name": map[string]interface{}{"type": "string", "description": "Allowlisted task config name."},
			"arguments":   map[string]interface{}{"type": "object", "description": "Exactly the declared parameters for that config."},
		},
		"required": []string{"config_name"},
	}
}
func (t *startTaskTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	m, err := t.h.manager()
	if err != nil {
		return "", err
	}
	for k := range args {
		if k != "config_name" && k != "arguments" {
			return "", fmt.Errorf("unexpected field %q", k)
		}
	}
	name, ok := args["config_name"].(string)
	if !ok || name == "" {
		return "", errors.New("config_name is required")
	}
	var a map[string]any
	if raw, present := args["arguments"]; present && raw != nil {
		if a, ok = raw.(map[string]any); !ok {
			return "", errors.New("arguments must be an object")
		}
	}
	st, err := m.Start(ctx, name, a)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(map[string]string{"task_id": st.ID, "status": "launched"})
	return string(b), nil
}

type taskStatusTool struct{ h *TaskHandle }

func (t *taskStatusTool) GetName() string { return "get_task_status" }
func (t *taskStatusTool) GetDescription() string {
	return "Get the short status (running, done, error, timeout, cancelled) and a bounded summary of a task started by start_task."
}
func (t *taskStatusTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"task_id": map[string]interface{}{"type": "string"}},
		"required":   []string{"task_id"},
	}
}
func (t *taskStatusTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	m, err := t.h.manager()
	if err != nil {
		return "", err
	}
	id, _ := args["task_id"].(string)
	st, ok := m.Status(id)
	if !ok {
		return "", fmt.Errorf("unknown task %q", id)
	}
	b, _ := json.Marshal(st)
	return string(b), nil
}
