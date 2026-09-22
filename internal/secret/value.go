// Package secret provides a type-level guard against a credential rakitsu
// itself holds (a provider API key, today) being accidentally printed,
// logged, or serialized through Go's normal formatting/encoding paths.
//
// This is a different layer from internal/telemetry's pattern-based
// redaction of tool-call arguments/output. That redaction defends against
// an unbounded surface — a secret a TOOL prints can take any shape, so it
// has to be recognized after the fact by pattern. A provider API key is a
// bounded case: rakitsu itself loads it, once, from the environment. For
// that case, Value makes the ordinary unsafe operation — formatting or
// JSON-encoding the raw string via a normal fmt/json call on a Value the
// caller actually holds — not work by construction.
//
// What this does NOT guarantee (verified, not just claimed): a Value
// stored in an UNEXPORTED struct field is invisible to Go's own reflective
// formatting — fmt cannot call a method on an unexported field, so it
// falls back to printing that field's underlying representation, raw
// string included. `fmt.Printf("%+v", struct{ key Value }{key: v})` prints
// the real value; the same struct with an EXPORTED `Key Value` field does
// not. A deliberate reflection-based dump helper (or a library like
// go-spew configured to bypass method invocation) can also read the
// unexported field directly. ALWAYS store Value in an exported field
// (`APIKey Value`, not `apiKey Value`), and never embed it anonymously —
// anonymous embedding promotes Reveal()/String()/Format()/MarshalJSON()
// onto the embedding type itself, which is very likely not what you want.
//
// This holds no encryption, no storage, no keys to manage — it is a
// language-level hygiene pattern (see Rust's `secrecy` crate, Pydantic's
// `SecretStr` for the same idea elsewhere), not a secrets manager. It
// raises the bar for accidental disclosure; it is not a guarantee against
// a determined reflective walker or an arbitrary custom serializer.
package secret

import (
	"encoding/json"
	"fmt"
)

const mask = "[REDACTED]"

// Value wraps a string so that fmt formatting (String, Format — every
// verb, including %v inside a struct or an error) and JSON encoding
// (MarshalJSON) return a fixed mask instead of the real value, as long as
// the Value is reachable through an exported field (see the package
// comment for the unexported-field exception). Reveal() is the one
// sanctioned way to get the real value out. The zero Value is valid and
// reveals as "".
type Value struct {
	raw string
}

// New wraps raw as a Value.
func New(raw string) Value {
	return Value{raw: raw}
}

// Reveal returns the raw credential, for handing to a provider SDK or
// building an auth header at the actual authentication boundary. The
// returned string is a plain string from this point on — Value provides
// no protection for it once revealed. Keep the reveal inline at the call
// site (`sdk.New(config.APIKey.Reveal())`, not `raw := ...Reveal(); later
// use raw`), don't log it, and don't assume the SDK/library it's handed to
// won't itself log its own config — that's a separate boundary this type
// can't see into.
func (v Value) Reveal() string {
	return v.raw
}

// IsEmpty reports whether the wrapped value is the empty string — the
// same check code would otherwise write as `v.Reveal() == ""`, spelled so
// nothing needs to call Reveal just to check for absence.
func (v Value) IsEmpty() bool {
	return v.raw == ""
}

// String implements fmt.Stringer. Always returns the mask, never the
// wrapped value, regardless of whether the value is empty.
func (v Value) String() string {
	return mask
}

// Format implements fmt.Formatter, which fmt.Sprintf and friends check
// BEFORE Stringer for every verb (%v, %+v, %s, %q, %#v, ...) once a type
// implements it — without this, %#v would print via GoStringer (which
// this type doesn't implement) or reflection instead of String(), still
// exposing raw. Format covers every verb for a DIRECTLY-held Value or one
// reachable through an exported field; see the package comment for the
// unexported-field case this can't cover (fmt never calls a method on an
// unexported field to begin with).
func (v Value) Format(f fmt.State, verb rune) {
	_, _ = f.Write([]byte(mask))
}

// MarshalJSON implements json.Marshaler. Always encodes as the mask
// string, never the wrapped value.
func (v Value) MarshalJSON() ([]byte, error) {
	return json.Marshal(mask)
}
