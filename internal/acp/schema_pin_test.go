package acp

import (
	"os"
	"strings"
	"testing"
)

// TestACPSchemaPin_PermissionShapes fails loudly if the pinned schema excerpt
// is re-pinned to a version that renames a field the permission flow relies on.
func TestACPSchemaPin_PermissionShapes(t *testing.T) {
	b, err := os.ReadFile("testdata/acp-schema-0.4.5/schema-excerpt.d.ts")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`session_request_permission: "session/request_permission"`,
		`session_cancel: "session/cancel"`,
		"export interface RequestPermissionRequest",
		"options: PermissionOption[];",
		"sessionId: string;",
		"toolCall: ToolCallUpdate;",
		`kind: "allow_once" | "allow_always" | "reject_once" | "reject_always";`,
		"optionId: string;",
		"name: string;",
		"export interface RequestPermissionResponse",
		`outcome: "cancelled";`,
		`outcome: "selected";`,
		"toolCallId: string;",
		`type: "content";`,
		`type: "diff";`,
		`type: "terminal";`,
		"newText: string;",
		"oldText?: string | null;",
		`export type ToolKind = "read" | "edit" | "delete" | "move" | "search" | "execute" | "think" | "fetch" | "switch_mode" | "other";`,
		`export type ToolCallStatus = "pending" | "in_progress" | "completed" | "failed";`,
		"export interface CancelNotification",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("pinned schema lacks %q", want)
		}
	}
}
