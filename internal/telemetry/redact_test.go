package telemetry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactEventPayload_ToolCallStart_MasksSensitiveArgs(t *testing.T) {
	payload, _ := json.Marshal(ToolCallStartPayload{
		ToolCallID: "tc-1",
		ToolName:   "cli",
		Arguments: map[string]interface{}{
			"command":  "curl",
			"token":    "sk-secret",
			"api_key":  "abc123",
			"password": "hunter2",
		},
	})

	out := RedactEventPayload(EventToolCallStart, payload)

	var got ToolCallStartPayload
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal redacted payload: %v", err)
	}
	if got.Arguments["command"] != "curl" {
		t.Fatalf("non-sensitive arg was altered: %+v", got.Arguments)
	}
	for _, key := range []string{"token", "api_key", "password"} {
		if got.Arguments[key] != redactedMask {
			t.Fatalf("arg %q not redacted: %+v", key, got.Arguments)
		}
	}
}

func TestRedactEventPayload_ThoughtEnd_MasksIntendedToolCallArgs(t *testing.T) {
	payload, _ := json.Marshal(ThoughtEndPayload{
		StructuredThought: StructuredThought{
			Reasoning: "calling the api",
			IntendedToolCalls: []ToolCallSignature{
				{Name: "http", Arguments: map[string]interface{}{
					"url":           "https://example.com",
					"Authorization": "Bearer xyz",
				}},
			},
		},
		Iteration: 1,
	})

	out := RedactEventPayload(EventThoughtEnd, payload)

	var got ThoughtEndPayload
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal redacted payload: %v", err)
	}
	args := got.IntendedToolCalls[0].Arguments
	if args["url"] != "https://example.com" {
		t.Fatalf("non-sensitive arg was altered: %+v", args)
	}
	if args["Authorization"] != redactedMask {
		t.Fatalf("Authorization arg not redacted: %+v", args)
	}
}

// TestRedactEventPayload_NestedSensitiveKeys_Masked regression-guards a
// finding from code review: redaction only walked the
// top level of Arguments, so a credential-shaped key nested inside an
// object or array (e.g. {"headers":{"Authorization":"..."}}) reached the
// sink unredacted.
func TestRedactEventPayload_NestedSensitiveKeys_Masked(t *testing.T) {
	payload := json.RawMessage(`{
		"tool_call_id": "tc-1",
		"tool_name": "http",
		"arguments": {
			"url": "https://example.com",
			"headers": {"Authorization": "Bearer xyz", "Accept": "application/json"},
			"retries": [{"api_key": "abc"}, {"note": "fine"}]
		}
	}`)

	out := RedactEventPayload(EventToolCallStart, payload)

	var got struct {
		Arguments struct {
			URL     string              `json:"url"`
			Headers map[string]string   `json:"headers"`
			Retries []map[string]string `json:"retries"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal redacted payload: %v", err)
	}
	if got.Arguments.URL != "https://example.com" {
		t.Fatalf("non-sensitive arg was altered: %+v", got.Arguments)
	}
	if got.Arguments.Headers["Authorization"] != redactedMask {
		t.Fatalf("nested Authorization not redacted: %+v", got.Arguments.Headers)
	}
	if got.Arguments.Headers["Accept"] != "application/json" {
		t.Fatalf("nested non-sensitive header altered: %+v", got.Arguments.Headers)
	}
	if got.Arguments.Retries[0]["api_key"] != redactedMask {
		t.Fatalf("nested-in-array api_key not redacted: %+v", got.Arguments.Retries)
	}
	if got.Arguments.Retries[1]["note"] != "fine" {
		t.Fatalf("nested-in-array non-sensitive value altered: %+v", got.Arguments.Retries)
	}
}

// TestRedactEventPayload_PreservesLargeIntegerPrecision regression-guards
// a finding from code review: decoding Arguments
// through a plain map[string]interface{} turns every JSON number into a
// float64, which loses precision above 2^53 and silently changes a
// non-sensitive argument's value on re-encode.
func TestRedactEventPayload_PreservesLargeIntegerPrecision(t *testing.T) {
	payload := json.RawMessage(`{"tool_call_id":"tc-1","tool_name":"cli","arguments":{"offset":9007199254740993}}`)

	out := RedactEventPayload(EventToolCallStart, payload)

	if !strings.Contains(string(out), `"offset":9007199254740993`) {
		t.Fatalf("large integer argument lost precision: %s", out)
	}
}

// TestRedactEventPayload_PreservesUnknownFields regression-guards the
// review's secondary point: decoding into the typed payload struct drops
// any field the struct doesn't declare. Operating on the raw JSON object
// instead must carry every field through untouched.
func TestRedactEventPayload_PreservesUnknownFields(t *testing.T) {
	payload := json.RawMessage(`{"tool_call_id":"tc-1","tool_name":"cli","arguments":{"token":"sk-1"},"future_field":"kept"}`)

	out := RedactEventPayload(EventToolCallStart, payload)

	if !strings.Contains(string(out), `"future_field":"kept"`) {
		t.Fatalf("unknown field was dropped: %s", out)
	}
	if !strings.Contains(string(out), `"token":"`+redactedMask+`"`) {
		t.Fatalf("token still not redacted: %s", out)
	}
}

func TestRedactEventPayload_UnrelatedEventType_ReturnsUnchanged(t *testing.T) {
	payload := json.RawMessage(`{"reasoning":"nothing to redact here"}`)

	out := RedactEventPayload(EventAgentStart, payload)

	if string(out) != string(payload) {
		t.Fatalf("unhandled event type should pass through unchanged, got %s", out)
	}
}

// TestRedactEventPayload_ToolCallEnd_MasksSecretShapedOutput closes a real
// gap: tool-call ARGUMENTS were redacted at the sink, but free-form tool
// OUTPUT (e.g. `cat .env`, `curl -v`) was not, so a printed credential
// landed in the session JSONL / hub stream verbatim.
func TestRedactEventPayload_ToolCallEnd_MasksSecretShapedOutput(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   []string // substrings that must NOT survive redaction
	}{
		{
			name:   "env-style assignment",
			output: "DB_PASSWORD=hunter2\nAPI_KEY=sk-abc123\nUNRELATED=fine",
			want:   []string{"hunter2", "sk-abc123"},
		},
		{
			name:   "authorization header",
			output: "> GET /v1/things\n> Authorization: Bearer sk-live-abcdef123456\n> Host: example.com",
			want:   []string{"sk-live-abcdef123456"},
		},
		{
			name:   "url userinfo password",
			output: "connecting to postgres://admin:s3cr3tpw@db.internal:5432/app",
			want:   []string{"s3cr3tpw"},
		},
		{
			name: "pem private key block",
			output: "reading key.pem:\n-----BEGIN RSA PRIVATE KEY-----\n" +
				"MIIBOgIBAAJBAK...redacted-test-fixture...\n" +
				"-----END RSA PRIVATE KEY-----\ndone",
			want: []string{"MIIBOgIBAAJBAK"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, _ := json.Marshal(ToolCallEndPayload{
				ToolCallID: "tc-1",
				ToolName:   "cli",
				Output:     tc.output,
			})

			out := RedactEventPayload(EventToolCallEnd, payload)

			var got ToolCallEndPayload
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unmarshal redacted payload: %v", err)
			}
			for _, secret := range tc.want {
				if strings.Contains(got.Output, secret) {
					t.Fatalf("secret %q survived redaction in output: %q", secret, got.Output)
				}
			}
		})
	}
}

func TestRedactEventPayload_ToolCallEnd_MasksSecretShapedError(t *testing.T) {
	payload, _ := json.Marshal(ToolCallEndPayload{
		ToolCallID: "tc-1",
		ToolName:   "cli",
		Error:      "request failed: Authorization: Bearer sk-live-oops\nconnection refused",
	})

	out := RedactEventPayload(EventToolCallEnd, payload)

	var got ToolCallEndPayload
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal redacted payload: %v", err)
	}
	if strings.Contains(got.Error, "sk-live-oops") {
		t.Fatalf("secret survived redaction in error: %q", got.Error)
	}
	if !strings.Contains(got.Error, "connection refused") {
		t.Fatalf("non-secret error detail was dropped: %q", got.Error)
	}
}

func TestRedactEventPayload_ToolCallEnd_PreservesBenignOutput(t *testing.T) {
	payload, _ := json.Marshal(ToolCallEndPayload{
		ToolCallID: "tc-1",
		ToolName:   "cli",
		Output:     "build succeeded in 3.2s, 0 warnings, exit code 0",
	})

	out := RedactEventPayload(EventToolCallEnd, payload)

	var got ToolCallEndPayload
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal redacted payload: %v", err)
	}
	if got.Output != "build succeeded in 3.2s, 0 warnings, exit code 0" {
		t.Fatalf("benign output was altered: %q", got.Output)
	}
}

func TestRedactEventPayload_MalformedPayload_ReturnsUnchanged(t *testing.T) {
	payload := json.RawMessage(`not json`)

	out := RedactEventPayload(EventToolCallStart, payload)

	if string(out) != string(payload) {
		t.Fatalf("malformed payload should pass through unchanged, got %s", out)
	}
}

// The cases below regression-guard bugs a coverage review (feeding the
// implementation to an independent adversarial reasoning pass) found in
// the first version of the TOOL_CALL_END free-text redaction: partial
// masking of multi-word/quoted values, rules crossing newlines, and a URL
// rule that could consume a port/path as if it were a password.

func TestRedactFreeText_MasksEntireLine_NotJustFirstToken(t *testing.T) {
	cases := map[string]string{
		`PASSWORD="correct horse battery staple"`:                                      "correct horse battery staple",
		`PASSWORD = 'alpha beta'`:                                                      "alpha beta",
		`Authorization: Digest username="alice", realm="private", response="deadbeef"`: "deadbeef",
	}
	for input, mustNotContain := range cases {
		got := redactFreeText(input)
		if strings.Contains(got, mustNotContain) {
			t.Fatalf("input %q: secret remainder %q survived: %q", input, mustNotContain, got)
		}
	}
}

func TestRedactFreeText_DoesNotCrossLines(t *testing.T) {
	got := redactFreeText("Authorization: raw-secret\nnext-field: harmless")
	if strings.Contains(got, "[REDACTED] harmless") {
		t.Fatalf("authorization rule leaked onto the next line: %q", got)
	}
	if !strings.Contains(got, "next-field: harmless") {
		t.Fatalf("unrelated next line was altered: %q", got)
	}

	got = redactFreeText("PASSWORD=\nharmless-next-line")
	if strings.Contains(got, "harmless-next-line"+redactedMask) || strings.Contains(got, redactedMask+"\nharmless") {
		t.Fatalf("empty assignment leaked into next line: %q", got)
	}
}

func TestRedactFreeText_URLRule_DoesNotConsumePortOrPath(t *testing.T) {
	got := redactFreeText("https://example.com:8443/users/alice@example.org")
	if strings.Contains(got, redactedMask) {
		t.Fatalf("url rule incorrectly matched a port/path as a password: %q", got)
	}
	if !strings.Contains(got, "8443") {
		t.Fatalf("benign port was altered: %q", got)
	}
}

func TestRedactFreeText_BearerRule_CoversRFC6750Alphabet(t *testing.T) {
	got := redactFreeText("Bearer abcdefgh+/SECRETTAIL==")
	if strings.Contains(got, "SECRETTAIL") {
		t.Fatalf("bearer token suffix survived (charset too narrow): %q", got)
	}
}

// TestRedactFreeText_BearerRule_MatchesShortTokens pins down a real gap
// found by external review on the public sync PR: RFC 6750 §2.1's token68
// grammar has no minimum length beyond 1 character, but the rule
// previously required 6+ characters, so a short-but-valid bearer token
// (e.g. from a test fixture or an internal service that issues short
// opaque tokens) passed through unredacted.
func TestRedactFreeText_BearerRule_MatchesShortTokens(t *testing.T) {
	cases := []string{"Bearer abc", "Bearer a1b2"}
	for _, in := range cases {
		got := redactFreeText(in)
		if got == in {
			t.Fatalf("short bearer token was not redacted: %q -> %q", in, got)
		}
	}
}

func TestRedactFreeText_JSONOutput_MasksCredentialShapedKeys(t *testing.T) {
	got := redactFreeText(`{"password":"hunter2","api_key":"opaque-token","note":"fine"}`)
	if strings.Contains(got, "hunter2") || strings.Contains(got, "opaque-token") {
		t.Fatalf("JSON-formatted output secret survived: %q", got)
	}
	if !strings.Contains(got, "fine") {
		t.Fatalf("non-secret JSON field was dropped: %q", got)
	}

	got = redactFreeText("{\n  \"password\": \"hunter2\"\n}")
	if strings.Contains(got, "hunter2") {
		t.Fatalf("pretty-printed JSON secret survived: %q", got)
	}
}

func TestRedactEventPayload_ToolCallEnd_NullFieldsStayNull(t *testing.T) {
	payload := json.RawMessage(`{"tool_call_id":"tc-1","tool_name":"cli","output":null,"error":null}`)

	out := RedactEventPayload(EventToolCallEnd, payload)

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal redacted payload: %v", err)
	}
	if string(obj["output"]) != "null" {
		t.Fatalf("null output field was changed to %s, want null", obj["output"])
	}
	if string(obj["error"]) != "null" {
		t.Fatalf("null error field was changed to %s, want null", obj["error"])
	}
}

// The cases below regression-guard a SECOND round of findings: a
// verification pass that fed the FIXED implementation back to the same
// adversarial reasoning process (rather than assuming the first round of
// fixes worked) found the fixes above had themselves introduced new bugs
// or left the coverage narrower than intended.

func TestRedactIfJSON_RequiresWholeStringToBeOneDocument(t *testing.T) {
	// json.Decoder.Decode reads only the next value, not the whole
	// input — without an explicit EOF check, trailing content
	// (including a secret) after a valid JSON prefix was silently
	// discarded rather than redacted.
	got := redactFreeText(`{"status":"ok"}
diagnostic text that must survive`)
	if !strings.Contains(got, "diagnostic text that must survive") {
		t.Fatalf("trailing non-JSON content after a JSON prefix was dropped: %q", got)
	}

	got = redactFreeText(`{"password":"hunter2"}{"result":42}`)
	if strings.Contains(got, "hunter2") {
		t.Fatalf("secret in first of two concatenated JSON docs survived: %q", got)
	}
	if !strings.Contains(got, "42") {
		t.Fatalf("second concatenated JSON doc was silently dropped: %q", got)
	}
}

func TestRedactFreeText_JSONFastPath_StillScansNonSensitiveKeyStrings(t *testing.T) {
	cases := map[string]string{
		`{"message":"Bearer abcdef123456"}`:                 "abcdef123456",
		`{"message":"postgres://alice:hunter2@db.example"}`: "hunter2",
		`["PASSWORD=hunter2"]`:                              "hunter2",
	}
	for input, mustNotContain := range cases {
		got := redactFreeText(input)
		if strings.Contains(got, mustNotContain) {
			t.Fatalf("input %q: secret %q inside a non-sensitive-keyed JSON string survived: %q", input, mustNotContain, got)
		}
	}
}

func TestRedactFreeText_LineRules_MatchMidLine_NotOnlyLineStart(t *testing.T) {
	cases := map[string]string{
		"> Authorization: Basic dXNlcjpwYXNz":                               "dXNlcjpwYXNz",
		"< authorization: Digest username=\"alice\", response=\"deadbeef\"": "deadbeef",
		"2026-01-01T00:00:00Z Authorization: Basic dXNlcjpwYXNz":            "dXNlcjpwYXNz",
		`export PASSWORD="correct horse battery staple"`:                    "correct horse battery staple",
		"connection failed: password=hunter2":                               "hunter2",
	}
	for input, mustNotContain := range cases {
		got := redactFreeText(input)
		if strings.Contains(got, mustNotContain) {
			t.Fatalf("input %q: secret %q survived (label not at line start): %q", input, mustNotContain, got)
		}
	}
}

func TestRedactFreeText_QuotedKeyFallback_CatchesMixedOrMalformedJSON(t *testing.T) {
	cases := map[string]string{
		`result: {"password":"hunter2"}`:            "hunter2",
		"```json\n{\"password\": \"hunter2\"}\n```": "hunter2",
		`{"password":"hunter2"`:                     "hunter2", // truncated, missing closing brace
	}
	for input, mustNotContain := range cases {
		got := redactFreeText(input)
		if strings.Contains(got, mustNotContain) {
			t.Fatalf("input %q: secret %q in mixed/malformed JSON survived: %q", input, mustNotContain, got)
		}
	}
}

func TestRedactFreeText_URLRule_DoesNotCrossQueryOrFragment(t *testing.T) {
	cases := []string{
		"https://example.com:443?contact=alice@example.net",
		"https://example.com:443#contact=alice@example.net",
	}
	for _, input := range cases {
		got := redactFreeText(input)
		if strings.Contains(got, redactedMask) {
			t.Fatalf("url rule incorrectly treated a port+query/fragment as a password: %q -> %q", input, got)
		}
	}
}

func TestRedactFreeText_URLRule_AllowsEmptyUsername(t *testing.T) {
	got := redactFreeText("postgres://:hunter2@db.example/app")
	if strings.Contains(got, "hunter2") {
		t.Fatalf("empty-username userinfo password survived: %q", got)
	}
}

func TestRedactFreeText_BearerRule_DoesNotCrossLines(t *testing.T) {
	got := redactFreeText("bearer\nordinary next line, not a token")
	if !strings.Contains(got, "ordinary next line, not a token") {
		t.Fatalf("bearer rule leaked across a newline: %q", got)
	}
}

func TestRedactFreeText_IsIdempotent(t *testing.T) {
	inputs := []string{
		"Authorization: Bearer sk-live-abcdef123456",
		"DB_PASSWORD=hunter2",
		"connecting to postgres://admin:s3cr3tpw@db.internal:5432/app",
	}
	for _, in := range inputs {
		once := redactFreeText(in)
		twice := redactFreeText(once)
		if once != twice {
			t.Fatalf("redaction not idempotent for %q: first pass %q, second pass %q", in, once, twice)
		}
	}
}

// TestRedactEventPayload_ToolCallStart_MasksAbbreviatedCredentialKeys
// regression-guards a gap found live: "pwd" is a very common real-world
// abbreviation for "password" (config files, connection strings, legacy
// code) but is not a substring of "password", so the original pattern
// missed it. "creds"/"credential(s)" are similarly common. Deliberately
// NOT adding bare "key" here — see the sensitiveArgKey doc comment for
// why that one stays out.
func TestRedactEventPayload_ToolCallStart_MasksAbbreviatedCredentialKeys(t *testing.T) {
	payload, _ := json.Marshal(ToolCallStartPayload{
		ToolCallID: "tc-1",
		ToolName:   "cli",
		Arguments: map[string]interface{}{
			"note":       "fine",
			"pwd":        "hunter2",
			"passwd":     "hunter2",
			"creds":      "hunter2",
			"credential": "hunter2",
		},
	})

	out := RedactEventPayload(EventToolCallStart, payload)

	var got ToolCallStartPayload
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal redacted payload: %v", err)
	}
	if got.Arguments["note"] != "fine" {
		t.Fatalf("non-sensitive arg was altered: %+v", got.Arguments)
	}
	for _, key := range []string{"pwd", "passwd", "creds", "credential"} {
		if got.Arguments[key] != redactedMask {
			t.Fatalf("arg %q not redacted: %+v", key, got.Arguments)
		}
	}
}

func TestRedactFreeText_MasksAbbreviatedCredentialAssignments(t *testing.T) {
	cases := map[string]string{
		"DB_PWD=hunter2":     "hunter2",
		"passwd=hunter2":     "hunter2",
		"creds=hunter2":      "hunter2",
		"credential=hunter2": "hunter2",
	}
	for input, mustNotContain := range cases {
		got := redactFreeText(input)
		if strings.Contains(got, mustNotContain) {
			t.Fatalf("input %q: secret %q survived: %q", input, mustNotContain, got)
		}
	}
}

// TestRedactFreeText_BareKey_StillNotMatched documents the deliberate
// choice NOT to add bare "key" to sensitiveArgKey: it's too generic a
// word (primary_key, row_key, key_id are all common non-secret field
// names) — matching it would trade a real gap for a much noisier false
// positive. If this test ever starts failing because someone added "key"
// to the pattern, that tradeoff decision needs revisiting deliberately,
// not accidentally.
func TestRedactFreeText_BareKey_StillNotMatched(t *testing.T) {
	got := redactFreeText(`{"key":"hunter2","note":"fine"}`)
	if !strings.Contains(got, "hunter2") {
		t.Fatalf("bare \"key\" started matching — if intentional, update this test's comment and the sensitiveArgKey doc comment together: %q", got)
	}
}

func TestSetExtraRedactKeywords_DefaultBehaviorUnaffectedWhenEmpty(t *testing.T) {
	t.Cleanup(func() { SetExtraRedactKeywords(nil) })
	SetExtraRedactKeywords(nil)

	got := redactFreeText(`{"token":"hunter2","note":"fine"}`)
	if strings.Contains(got, "hunter2") {
		t.Fatalf("built-in keyword stopped matching after SetExtraRedactKeywords(nil): %q", got)
	}
	got = redactFreeText(`{"internal_project_code":"hunter2","note":"fine"}`)
	if !strings.Contains(got, "hunter2") {
		t.Fatalf("non-keyword field got redacted with no extra keywords set: %q", got)
	}
}

func TestSetExtraRedactKeywords_CustomKeyword_RedactsStructuredArg(t *testing.T) {
	t.Cleanup(func() { SetExtraRedactKeywords(nil) })
	SetExtraRedactKeywords([]string{"internal_project_code"})

	payload, _ := json.Marshal(ToolCallStartPayload{
		ToolCallID: "tc-1",
		ToolName:   "cli",
		Arguments: map[string]interface{}{
			"internal_project_code": "hunter2",
			"note":                   "fine",
		},
	})
	out := RedactEventPayload(EventToolCallStart, payload)
	if strings.Contains(string(out), "hunter2") {
		t.Fatalf("custom keyword did not redact structured argument: %s", out)
	}
	if !strings.Contains(string(out), "fine") {
		t.Fatalf("unrelated argument got redacted too: %s", out)
	}
}

func TestSetExtraRedactKeywords_CustomKeyword_RedactsFreeText(t *testing.T) {
	t.Cleanup(func() { SetExtraRedactKeywords(nil) })
	SetExtraRedactKeywords([]string{"internal_project_code"})

	got := redactFreeText("internal_project_code=hunter2")
	if strings.Contains(got, "hunter2") {
		t.Fatalf("custom keyword did not redact free-text assignment: %q", got)
	}
}

func TestSetExtraRedactKeywords_RegexSpecialCharsTreatedLiterally(t *testing.T) {
	t.Cleanup(func() { SetExtraRedactKeywords(nil) })
	SetExtraRedactKeywords([]string{"x.y+z"})

	// The literal keyword must match; a lookalike where "." acted as
	// "any character" (regex metacharacter) must NOT also match.
	got := redactFreeText("x.y+z=hunter2")
	if strings.Contains(got, "hunter2") {
		t.Fatalf("literal custom keyword did not redact: %q", got)
	}
	got = redactFreeText("xAy+z=hunter2")
	if !strings.Contains(got, "hunter2") {
		t.Fatalf("custom keyword's \".\" was interpreted as regex wildcard, not literal: %q", got)
	}
}

func TestSetExtraRedactKeywords_ReplacesPreviousCustomList(t *testing.T) {
	t.Cleanup(func() { SetExtraRedactKeywords(nil) })
	SetExtraRedactKeywords([]string{"firstword"})
	SetExtraRedactKeywords([]string{"secondword"})

	got := redactFreeText("firstword=hunter2")
	if !strings.Contains(got, "hunter2") {
		t.Fatalf("stale keyword from a previous SetExtraRedactKeywords call still matched: %q", got)
	}
	got = redactFreeText("secondword=hunter2")
	if strings.Contains(got, "hunter2") {
		t.Fatalf("latest custom keyword did not redact: %q", got)
	}
}
