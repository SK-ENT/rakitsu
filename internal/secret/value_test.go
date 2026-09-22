package secret

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const rawSecret = "sk-live-do-not-print-me"

func TestValue_Reveal_ReturnsOriginal(t *testing.T) {
	v := New(rawSecret)
	if v.Reveal() != rawSecret {
		t.Fatalf("Reveal() = %q, want %q", v.Reveal(), rawSecret)
	}
}

func TestValue_String_IsExactlyTheMask(t *testing.T) {
	v := New(rawSecret)
	if v.String() != mask {
		t.Fatalf("String() = %q, want exactly %q", v.String(), mask)
	}
}

// TestValue_Sprintf_IsExactlyTheMask asserts exact equality, not just
// strings.Contains(got, rawSecret) == false — a substring check would
// also pass for a bug that returns "" or a truncated prefix instead of
// the intended fixed mask, silently changing the contract.
func TestValue_Sprintf_IsExactlyTheMask(t *testing.T) {
	v := New(rawSecret)
	cases := []string{"%v", "%+v", "%s", "%q", "%#v"}
	for _, verb := range cases {
		got := fmt.Sprintf(verb, v)
		if got != mask {
			t.Fatalf("fmt.Sprintf(%q, v) = %q, want exactly %q", verb, got, mask)
		}
	}
}

func TestValue_JSONMarshal_IsExactlyTheMask(t *testing.T) {
	v := New(rawSecret)
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	want := `"` + mask + `"`
	if string(out) != want {
		t.Fatalf("json.Marshal = %s, want exactly %s", out, want)
	}
}

// TestValue_ContainedInExportedField_DoesNotLeak covers a Value stored as
// a NAMED, EXPORTED struct field — the way it's actually used in this
// codebase (llm.ProviderConfig.APIKey). This is NOT Go struct embedding
// (an anonymous field, `Value` with no field name, which would instead
// promote Reveal/String/Format/MarshalJSON onto the containing type) —
// see TestValue_UnexportedField_IsADocumentedLimitation for the real
// remaining gap this does NOT cover.
func TestValue_ContainedInExportedField_DoesNotLeak(t *testing.T) {
	type providerConfig struct {
		Name   string
		APIKey Value
	}
	pc := providerConfig{Name: "openai", APIKey: New(rawSecret)}

	sprintfOut := fmt.Sprintf("%+v", pc)
	if strings.Contains(sprintfOut, rawSecret) {
		t.Fatalf("leaked via fmt.Sprintf: %q", sprintfOut)
	}

	jsonOut, err := json.Marshal(pc)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(jsonOut), rawSecret) {
		t.Fatalf("leaked via json.Marshal: %s", jsonOut)
	}

	// The struct's own field name must still round-trip normally — only
	// the secret value itself is masked, not the whole struct.
	if !strings.Contains(string(jsonOut), "openai") {
		t.Fatalf("non-secret sibling field was dropped: %s", jsonOut)
	}
}

// TestValue_UnexportedField_IsADocumentedLimitation demonstrates, rather
// than guards against, a real gap found by adversarial review and
// verified against actual Go semantics: fmt never invokes a method on an
// UNEXPORTED struct field (it can't obtain an interface for it the normal
// way), so it falls back to printing that field's underlying
// representation — the raw secret — instead of calling Value.Format. A
// Value reachable only through an unexported field is NOT protected by
// this package; see the package doc comment. This test exists so that if
// a future Go version or fmt change ever closes this gap, someone notices
// (via this test's assumption breaking) rather than the documented
// limitation silently going stale.
func TestValue_UnexportedField_IsADocumentedLimitation(t *testing.T) {
	holder := struct{ key Value }{key: New(rawSecret)}

	got := fmt.Sprintf("%+v", holder)
	if !strings.Contains(got, rawSecret) {
		t.Fatalf("expected the documented unexported-field gap to still reproduce (got %q) — "+
			"if this now fails, Go's fmt behavior changed and the package doc comment's "+
			"limitation notice should be updated/removed", got)
	}
}

func TestValue_EmptyValue_RevealsEmptyNotMask(t *testing.T) {
	var v Value
	if v.Reveal() != "" {
		t.Fatalf("zero-value Reveal() = %q, want empty string", v.Reveal())
	}
}

func TestValue_IsEmpty(t *testing.T) {
	var zero Value
	if !zero.IsEmpty() {
		t.Fatalf("zero Value should report IsEmpty() == true")
	}
	if New(rawSecret).IsEmpty() {
		t.Fatalf("non-empty Value should report IsEmpty() == false")
	}
}

func TestValue_InOrdinaryContainers_DoesNotLeak(t *testing.T) {
	v := New(rawSecret)

	slice := fmt.Sprintf("%+v", []Value{v})
	if strings.Contains(slice, rawSecret) {
		t.Fatalf("leaked from a slice: %q", slice)
	}

	m := fmt.Sprintf("%+v", map[string]Value{"api_key": v})
	if strings.Contains(m, rawSecret) {
		t.Fatalf("leaked from a map value: %q", m)
	}

	ptr := fmt.Sprintf("%+v", &v)
	if strings.Contains(ptr, rawSecret) {
		t.Fatalf("leaked through a pointer: %q", ptr)
	}
}

func TestValue_ErrorWrapping_IsExactlyTheMask(t *testing.T) {
	v := New(rawSecret)
	err := fmt.Errorf("auth failed for key %v", v)
	want := "auth failed for key " + mask
	if err.Error() != want {
		t.Fatalf("err.Error() = %q, want exactly %q", err.Error(), want)
	}
}
