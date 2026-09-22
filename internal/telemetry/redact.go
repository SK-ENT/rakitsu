package telemetry

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"sync"
)

const redactedMask = "[REDACTED]"

// credentialKeyword is the single source of truth for "does this key/label
// look credential-shaped" — a regexp alternation fragment (no (?i), no
// enclosing group; callers wrap it as needed) shared by every pattern that
// needs this list: sensitiveArgKey below, and two of the freeTextRules
// entries (the KEY=value/KEY: value assignment rule and the quoted-key
// JSON fallback rule). Previously each of those three had its own
// hand-copied alternation, which is exactly how a real gap happened: "pwd"
// was added to sensitiveArgKey but not to the other two copies, so
// `DB_PWD=hunter2` in free-form tool output stayed unredacted even after
// `{"pwd": "hunter2"}` in structured arguments was fixed. One shared
// fragment makes that class of drift impossible instead of relying on
// remembering to update every copy.
//
// Includes common real-world abbreviations of the full words ("pwd"/
// "passwd" for password, "creds"/"credential" for credential(s)) — found
// live: "pwd" isn't a substring of "password", so it slipped through
// until added explicitly.
//
// Deliberately does NOT include bare "key" (unqualified, not "api_key"/
// "api-key"): it's too generic a word — "primary_key", "row_key", "key_id"
// are all common non-secret field names, and matching it would trade a
// real gap for a much noisier false-positive rate. See
// TestRedactFreeText_BareKey_StillNotMatched, which pins this down as a
// deliberate choice, not an oversight, and would need updating alongside
// any future change to this decision. Considered and rejected for the
// same reason: fuzzy/edit-distance matching on short key names (e.g. to
// auto-catch typos) — edit-distance 1 from "key" alone includes "kex",
// "ket", "hey", "keg", "kay", "kev" and more, ordinary words with no
// connection to secrets.
const credentialKeyword = `token|api[_-]?key|password|pwd|passwd|secret|credential|creds|authorization`

// sensitiveArgKey matches tool-call argument keys that commonly carry
// credentials, so their values can be masked before an event reaches a
// telemetry sink (session JSONL, hub stream). Case-insensitive substring
// match, so e.g. "auth_token" and "db_password" are caught too, not just
// exact keys.
//
// Built (along with two of the freeTextRules entries below) from
// credentialKeyword plus any operator-supplied extra keywords set via
// SetExtraRedactKeywords — see redactMu/rebuildRedactPatterns.
var sensitiveArgKey = regexp.MustCompile(`(?i)` + credentialKeyword)

// redactMu guards sensitiveArgKey and freeTextRules against concurrent
// reads (every RedactEventPayload call, on the hot path for every tool
// event) racing a SetExtraRedactKeywords rebuild. SetExtraRedactKeywords
// is expected to be called once, at startup after config load, but the
// guard costs nothing on the read side worth avoiding.
var redactMu sync.RWMutex

// SetExtraRedactKeywords adds operator-supplied keywords (config's
// settings.redact_keywords) to the built-in credential-keyword detection
// used by both structured-argument redaction (sensitiveArgKey) and the
// two keyword-aware freeTextRules entries, and recompiles all three.
// Each call REPLACES the previous extra-keyword list; it does not
// accumulate across calls.
//
// Each word is escaped with regexp.QuoteMeta before being spliced into
// the pattern, so e.g. "x.y" matches only the literal substring "x.y",
// never "x" followed by any character.
func SetExtraRedactKeywords(words []string) {
	rebuildRedactPatterns(words)
}

func rebuildRedactPatterns(extra []string) {
	keyword := credentialKeyword
	for _, w := range extra {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		keyword += "|" + regexp.QuoteMeta(w)
	}

	argKey := regexp.MustCompile(`(?i)` + keyword)
	rules := buildFreeTextRules(keyword)

	redactMu.Lock()
	sensitiveArgKey = argKey
	freeTextRules = rules
	redactMu.Unlock()
}

// freeTextRule is one best-effort detector for secret-shaped substrings in
// free-form text, paired with the exact Go regexp replacement template for
// it. The template is spelled out per rule rather than inferred from
// pattern.NumSubexp() — a generic "N groups means preserve N segments"
// convention silently breaks (and can re-emit the secret it was supposed
// to mask) if a future rule's capture-group count doesn't match what the
// dispatch code assumed. Explicit per-rule templates can't drift that way.
type freeTextRule struct {
	pattern  *regexp.Regexp
	template string
}

// freeTextRules are best-effort detectors for secret-shaped substrings
// inside free-form tool output/error text (TOOL_CALL_END.Output/.Error),
// which — unlike tool-call arguments — is not a key/value map, so
// sensitiveArgKey's key-name matching cannot apply to it directly. This is
// deliberately narrow and pattern-based (not general entropy scanning): a
// telemetry hot path favors few false positives over broad coverage.
//
// Rules match the label/key ANYWHERE in a line (a leading \b, not a `^`
// anchor) — an earlier version anchored to line start, which missed
// entirely ordinary formats like curl's `> Authorization: ...` trace
// prefix or `export PASSWORD=...`. Every rule still masks the ENTIRE rest
// of its line, not just the first whitespace-delimited token — stopping
// at the first token leaves a quoted multi-word value (e.g.
// `PASSWORD="correct horse battery staple"`) mostly intact. They use
// horizontal whitespace ([\t ]) around the key/label, and rely on `.` not
// matching newlines (no (?s) flag) plus `(?m)`'s per-line `$`, so a rule
// can't cross into the next line the way an unrestricted \s+/\s* would.
//
// See the known limits: this does not survive encoding/splitting, cannot
// see credentials it has no known shape for, and the quoted-key rule below
// is a bounded textual fallback for malformed/partial JSON, not a JSON
// parser.
//
// Holds the currently active rule set, built by buildFreeTextRules and
// replaced wholesale by rebuildRedactPatterns. Reads/writes are guarded
// by redactMu.
var freeTextRules = buildFreeTextRules(credentialKeyword)

// buildFreeTextRules constructs the freeTextRules set for a given
// credential-keyword alternation fragment (the built-in one, or that
// fragment extended with operator-supplied keywords — see
// rebuildRedactPatterns). Only two of the six rules key off it; the rest
// are keyword-independent and identical across every call.
func buildFreeTextRules(keyword string) []freeTextRule {
	return []freeTextRule{
		{
			// PEM-style private-key blocks (RSA/EC/OPENSSH/PKCS8/encrypted).
			pattern:  regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
			template: redactedMask,
		},
		{
			// "Authorization: <anything>" anywhere on a line (curl -v/HTTP
			// trace output, log lines with a timestamp prefix, etc.). Masks
			// the whole value, not just its first token, so a multi-word
			// scheme like `Digest username="...", response="..."` doesn't
			// leak everything after the first space.
			pattern:  regexp.MustCompile(`(?im)(\bauthorization[\t ]*:[\t ]*)\S.*$`),
			template: "${1}" + redactedMask,
		},
		{
			// Bearer tokens appearing outside a labeled Authorization line.
			// Alphabet is informed by (not a full implementation of) RFC 6750
			// §2.1's token68 (letters, digits, -._~+/, optional = padding);
			// [\t ]+ (not \s+) between "bearer" and the token keeps this rule
			// from crossing a newline. The whole "bearer <token>" phrase is
			// masked together.
			//
			// No practical minimum length: token68 itself has none beyond 1
			// character, and this is a security-redaction rule, where the
			// correct failure mode is over-redaction (occasionally masking
			// an ordinary word after "bearer" in prose) rather than
			// under-redaction (missing a real short token). An earlier
			// {6,} floor let e.g. "Bearer abc" pass through unredacted —
			// found by external review on the public sync PR. The design
			// already accepted the over-redaction trade-off at the word
			// level (any sufficiently long alnum run after "bearer" was
			// already masked whether or not it looked token-shaped); this
			// just extends the same trade-off to shorter strings too.
			pattern:  regexp.MustCompile(`(?i)\bbearer[\t ]+[a-z0-9\-._~+/]+={0,2}`),
			template: redactedMask,
		},
		{
			// KEY=value / KEY: value anywhere on a line whose key looks
			// credential-shaped, the free-text analogue of sensitiveArgKey
			// (e.g. a printed .env file, `export KEY=...`, or an inline
			// `connection failed: password=...` log message). Masks the
			// whole rest of the line.
			pattern:  regexp.MustCompile(`(?im)(\b[\w.-]*(?:` + keyword + `)[\w.-]*[\t ]*[:=][\t ]*)\S.*$`),
			template: "${1}" + redactedMask,
		},
		{
			// A quoted JSON-style "key":"value" pair whose key looks
			// credential-shaped, wherever it appears in the text — a bounded
			// textual fallback (not a JSON parser) for output that contains
			// JSON but isn't itself one clean top-level document: prose
			// wrapping a JSON blob, a markdown-fenced ```json block, or
			// truncated/malformed JSON that redactIfJSON below therefore
			// declines to touch. Value class stops at an unescaped quote, so
			// it can't run past the value into the rest of the line.
			pattern:  regexp.MustCompile(`(?i)("[\w.-]*(?:` + keyword + `)[\w.-]*"[\t ]*:[\t ]*")(?:[^"\\]|\\.)*"`),
			template: "${1}" + redactedMask + `"`,
		},
		{
			// userinfo password in a connection-string/URL, e.g.
			// postgres://user:password@host. Username/password character
			// classes exclude "/", "?", and "#", so this can't run past the
			// authority into a path, query, or fragment and mistake a port or
			// `?key=value@host`-shaped query string for a password. Username
			// may be empty (some connection-string dialects allow `://:pw@`).
			pattern:  regexp.MustCompile(`(?i)(://[^/\s:?#@]*:)[^/\s?#@]+@`),
			template: "${1}" + redactedMask + "@",
		},
	}
}

// redactFreeText masks secret-shaped content in free-form text.
//
// If the text is ENTIRELY one JSON document — the common case for a `cli`
// tool wrapping an API call, or an `mcp_server` tool whose result is JSON
// — it is parsed and redacted with the same key-name rule used for
// tool-call arguments (redactValue), which catches e.g.
// {"password":"..."} however it's formatted/indented, PLUS every other
// string leaf is also run through freeTextRules (redactJSONLeafText) —
// otherwise a secret sitting under a non-sensitive-looking key, e.g.
// {"message":"Bearer sk-..."}, would survive the JSON fast path untouched.
//
// Anything that isn't exactly one JSON document — plain prose, curl -v
// trace output, a printed .env file, JSON with trailing/leading non-JSON
// content, malformed or truncated JSON — falls through to freeTextRules,
// which includes a bounded quoted-key fallback for exactly that case.
//
// This has no key name to key off in the free-text path — it can only
// recognize specific known shapes, so it is a best-effort defense-in-depth
// layer, not a guarantee that no secret can survive in arbitrary tool
// output.
func redactFreeText(s string) string {
	if s == "" {
		return s
	}
	if redacted, ok := redactIfJSON(s); ok {
		return redacted
	}
	return applyFreeTextRules(s)
}

// applyFreeTextRules runs every freeTextRules entry over s in order and
// returns the result. Separated out so redactJSONLeafText can apply the
// same rules to individual JSON string leaves without re-attempting JSON
// detection on each one (a leaf string that happens to look JSON-ish is
// not itself a document to parse — that would risk double-decoding and
// needs no depth/resource limits a recursive redactFreeText call would).
func applyFreeTextRules(s string) string {
	redactMu.RLock()
	rules := freeTextRules
	redactMu.RUnlock()
	for _, rule := range rules {
		s = rule.pattern.ReplaceAllString(s, rule.template)
	}
	return s
}

// redactIfJSON returns (redacted, true) if s is EXACTLY one JSON
// object/array document (nothing before or after it but whitespace),
// applying redactJSONLeafText. Returns ("", false) for anything else, so
// the caller falls back to line-oriented pattern matching.
//
// json.Decoder.Decode reads only the NEXT JSON value from the stream, not
// necessarily the whole input — silently ignoring trailing bytes. Without
// the second Decode-must-hit-EOF check below, `{"a":1}<leaked secret>` or
// two concatenated JSON documents would be "successfully" parsed as if the
// first document were the entire string, and everything after it —
// including a secret — would be silently discarded rather than redacted.
func redactIfJSON(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return "", false
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var val interface{}
	if err := dec.Decode(&val); err != nil {
		return "", false
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return "", false
	}
	out, err := json.Marshal(redactJSONLeafText(val))
	if err != nil {
		return "", false
	}
	return string(out), true
}

// RedactEventPayload masks credential-shaped argument values inside an
// event payload before it reaches a persistence or forwarding sink
// (internal/store.SessionStore.WriteEvent, HubClient.forwardEvents).
// Mirrors config.Redacted()'s shape: locate the field(s) that carry
// arguments, mask matched keys anywhere in them (including nested inside
// objects/arrays, e.g. {"headers":{"Authorization":"..."}}), re-encode.
//
// Everything outside the targeted field(s) is left as untouched raw JSON
// bytes — unknown/future fields survive, and numbers inside the targeted
// field are decoded with json.Number so re-encoding doesn't lose
// precision the way plain map[string]interface{} (which decodes all
// numbers as float64) would for large integers.
//
// Scope: TOOL_CALL_START.Arguments and THOUGHT_END's
// IntendedToolCalls[*].Arguments get key-name-based masking (redactValue).
// TOOL_CALL_END.Output/.Error is free-form text, not a key/value map, so it
// gets pattern-based best-effort masking instead (redactFreeText) — see
// that function's doc comment for what it can and cannot catch.
//
// On any decode error the input is returned unchanged — never drop or
// corrupt an event over a redaction failure.
func RedactEventPayload(eventType EventType, payload json.RawMessage) json.RawMessage {
	switch eventType {
	case EventToolCallStart:
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(payload, &obj); err != nil {
			return payload
		}
		redactJSONField(obj, "arguments")
		out, err := json.Marshal(obj)
		if err != nil {
			return payload
		}
		return out

	case EventThoughtEnd:
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(payload, &obj); err != nil {
			return payload
		}
		if raw, ok := obj["intended_tool_calls"]; ok {
			var calls []map[string]json.RawMessage
			if err := json.Unmarshal(raw, &calls); err == nil {
				for i := range calls {
					redactJSONField(calls[i], "arguments")
				}
				if out, err := json.Marshal(calls); err == nil {
					obj["intended_tool_calls"] = out
				}
			}
		}
		out, err := json.Marshal(obj)
		if err != nil {
			return payload
		}
		return out

	case EventToolCallEnd:
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(payload, &obj); err != nil {
			return payload
		}
		redactFreeTextField(obj, "output")
		redactFreeTextField(obj, "error")
		out, err := json.Marshal(obj)
		if err != nil {
			return payload
		}
		return out

	default:
		return payload
	}
}

// redactFreeTextField decodes obj[field] as a JSON string, applies
// redactFreeText, and writes the result back. No-op if the field is
// absent, JSON null, or not a string — mirrors redactJSONField's
// leave-untouched-on-any-doubt behavior. The explicit null check matters:
// json.Unmarshal(null, &s) succeeds and leaves s as "", so without it a
// null field would silently turn into an empty string.
func redactFreeTextField(obj map[string]json.RawMessage, field string) {
	raw, ok := obj[field]
	if !ok {
		return
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || string(trimmed) == "null" {
		return
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return
	}
	out, err := json.Marshal(redactFreeText(s))
	if err != nil {
		return
	}
	obj[field] = out
}

// redactJSONField decodes obj[field] (a JSON object), recursively masks
// any credential-shaped key in it, and writes the result back into
// obj[field] as re-encoded JSON. No-op if the field is absent or fails to
// decode — the caller's raw bytes for it are left untouched.
func redactJSONField(obj map[string]json.RawMessage, field string) {
	raw, ok := obj[field]
	if !ok {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // preserve large-integer precision through the round-trip
	var val interface{}
	if err := dec.Decode(&val); err != nil {
		return
	}
	out, err := json.Marshal(redactValue(val))
	if err != nil {
		return
	}
	obj[field] = out
}

// currentSensitiveArgKey returns the currently active sensitiveArgKey
// pattern under redactMu's read lock. The returned *regexp.Regexp is
// immutable and safe to keep using after the lock is released — only the
// package-level variable itself (the pointer) is ever reassigned, never
// mutated in place.
func currentSensitiveArgKey() *regexp.Regexp {
	redactMu.RLock()
	defer redactMu.RUnlock()
	return sensitiveArgKey
}

// redactValue walks a value decoded with json.Decoder.UseNumber
// (map[string]interface{}, []interface{}, json.Number, string, bool, or
// nil) and returns a copy with any value masked whose containing map key
// looks credential-shaped, at any nesting depth.
func redactValue(v interface{}) interface{} {
	return redactValueWithKey(v, currentSensitiveArgKey())
}

func redactValueWithKey(v interface{}, key *regexp.Regexp) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			if key.MatchString(k) {
				out[k] = redactedMask
			} else {
				out[k] = redactValueWithKey(val, key)
			}
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = redactValueWithKey(val, key)
		}
		return out
	default:
		return v
	}
}

// redactJSONLeafText walks a value the same way redactValue does, but in
// addition to masking sensitive-key values, it also runs every remaining
// string leaf through applyFreeTextRules — otherwise a secret sitting in a
// JSON field whose key isn't itself credential-shaped, e.g.
// {"message":"Bearer sk-live-..."} or {"note":"postgres://a:b@host"},
// would survive the JSON fast path in redactFreeText untouched (it would
// still get caught if the WHOLE output fell through to freeTextRules, but
// a successful JSON parse currently short-circuits that fallback).
//
// Used only for TOOL_CALL_END.Output/.Error (via redactIfJSON), not for
// tool-call ARGUMENTS (redactValue, via redactJSONField) — an argument's
// non-sensitive value is expected to round-trip byte-for-byte, and
// scanning it for secret-shaped substrings is a different, not-yet-made
// tradeoff for that path.
func redactJSONLeafText(v interface{}) interface{} {
	return redactJSONLeafTextWithKey(v, currentSensitiveArgKey())
}

func redactJSONLeafTextWithKey(v interface{}, key *regexp.Regexp) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			if key.MatchString(k) {
				out[k] = redactedMask
			} else {
				out[k] = redactJSONLeafTextWithKey(val, key)
			}
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = redactJSONLeafTextWithKey(val, key)
		}
		return out
	case string:
		return applyFreeTextRules(t)
	default:
		return v
	}
}
