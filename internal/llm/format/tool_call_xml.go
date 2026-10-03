package format

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// Qwen3-Coder text tool-call protocol:
//
//	<tool_call>
//	<function=NAME>
//	<parameter=KEY>
//	VALUE
//	</parameter>
//	</function>
//	</tool_call>
//
// The surrounding <tool_call> tags are optional here: models (and servers that
// strip the opening tag) have been seen emitting only the closing tag.
//
// Safety: parsing is strict and bounded, and a block only becomes a tool call
// when its name is registered for the request (FormatState.allowedTools).
// Anything else stays in the content as text.
const (
	maxToolCallXMLBytes  = 64 * 1024 // content larger than this is never parsed
	maxToolCallXMLCalls  = 32
	maxToolCallXMLParams = 64
)

var (
	xmlFunctionRegex = regexp.MustCompile(`(?s)(?:<tool_call>\s*)?<function=([^>]*)>(.*?)</function>\s*(?:</tool_call>)?`)
	xmlParamRegex    = regexp.MustCompile(`(?s)<parameter=([^>]*)>(.*?)</parameter>`)
	xmlNameRegex     = regexp.MustCompile(`^[A-Za-z0-9_.:\-]{1,128}$`)
	unparsedCallRe   = regexp.MustCompile(`<function=[A-Za-z0-9_.:\-]+>|</?tool_call>`)
)

// ContainsUnparsedToolCall reports whether text still looks like a tool-call
// block (Qwen-style <function=NAME> or a <tool_call> tag). The agent loop uses
// it to avoid reporting a turn as successful when a call was left as text.
func ContainsUnparsedToolCall(text string) bool {
	return unparsedCallRe.MatchString(text)
}

// parseXMLFunctionCalls extracts Qwen3-Coder style calls for allowed tool
// names. It returns the calls and the content with only the parsed blocks
// removed. On any limit violation it returns no calls and the original text.
func parseXMLFunctionCalls(content string, allowed map[string]struct{}) ([]llm.ToolCall, string) {
	if len(allowed) == 0 || len(content) > maxToolCallXMLBytes {
		return nil, content
	}
	locs := xmlFunctionRegex.FindAllStringSubmatchIndex(content, -1)
	if len(locs) == 0 {
		return nil, content
	}
	if len(locs) > maxToolCallXMLCalls {
		return nil, content
	}

	var calls []llm.ToolCall
	var cleaned strings.Builder
	last := 0
	for _, m := range locs {
		name := content[m[2]:m[3]]
		body := content[m[4]:m[5]]
		if _, ok := allowed[name]; !ok || !xmlNameRegex.MatchString(name) {
			continue
		}
		args, ok := parseXMLParams(body)
		if !ok {
			continue
		}
		cleaned.WriteString(content[last:m[0]])
		last = m[1]
		calls = append(calls, llm.ToolCall{
			ID:        fmt.Sprintf("content_tc_%d", len(calls)),
			Name:      name,
			Arguments: llm.NormalizeArgKeys(args),
		})
	}
	if len(calls) == 0 {
		return nil, content
	}
	cleaned.WriteString(content[last:])
	return calls, strings.TrimSpace(cleaned.String())
}

// parseXMLParams parses <parameter=K>V</parameter> children. Only whitespace
// may sit between them; anything else makes the block malformed.
func parseXMLParams(body string) (map[string]interface{}, bool) {
	locs := xmlParamRegex.FindAllStringSubmatchIndex(body, -1)
	if len(locs) > maxToolCallXMLParams {
		return nil, false
	}
	args := make(map[string]interface{}, len(locs))
	last := 0
	for _, m := range locs {
		if strings.TrimSpace(body[last:m[0]]) != "" {
			return nil, false
		}
		last = m[1]
		key := body[m[2]:m[3]]
		if !xmlNameRegex.MatchString(key) {
			return nil, false
		}
		// Drop the single newline Qwen puts after the open tag and before the
		// close tag; keep all other whitespace in the value intact.
		val := body[m[4]:m[5]]
		val = strings.TrimPrefix(val, "\n")
		val = strings.TrimSuffix(val, "\n")
		args[key] = val
	}
	if strings.TrimSpace(body[last:]) != "" {
		return nil, false
	}
	return args, true
}
