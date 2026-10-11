// Package claude runs `claude -p` in stream-json mode and decodes its events.
package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Event is one line of `claude -p --output-format stream-json --verbose`
// output. Only the fields the agent uses are decoded.
type Event struct {
	Type    string `json:"type"`    // system, assistant, user, stream_event, result, ...
	Subtype string `json:"subtype"` // init, success, error_* ...

	SessionID string `json:"session_id"`

	// assistant / user
	Message *Message `json:"message,omitempty"`

	// stream_event (with --include-partial-messages)
	Event *StreamEvent `json:"event,omitempty"`

	// result
	Result       string   `json:"result"`
	IsError      bool     `json:"is_error"`
	TotalCostUSD float64  `json:"total_cost_usd"`
	NumTurns     int      `json:"num_turns"`
	Errors       []string `json:"errors,omitempty"`
	// StructuredOutput is set with --json-schema.
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
}

// Message is an Anthropic message as echoed by claude.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// UnmarshalJSON accepts content as either a string or a block array. A
// message that is itself a string (system events such as permission_denied
// carry a human-readable "message") decodes as empty.
func (m *Message) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return nil
	}
	var raw struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	if len(raw.Content) == 0 || raw.Content[0] != '[' {
		var s string
		if len(raw.Content) > 0 {
			_ = json.Unmarshal(raw.Content, &s)
		}
		m.Content = []ContentBlock{{Type: "text", Text: s}}
		return nil
	}
	return json.Unmarshal(raw.Content, &m.Content)
}

// ContentBlock is a text, thinking, tool_use or tool_result block.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// StreamEvent is a raw Anthropic SSE event.
type StreamEvent struct {
	Type         string        `json:"type"` // message_start, content_block_start, content_block_delta, ...
	Index        int           `json:"index"`
	ContentBlock *ContentBlock `json:"content_block,omitempty"`
	Delta        *Delta        `json:"delta,omitempty"`
}

// Delta is a content_block_delta payload.
type Delta struct {
	Type        string `json:"type"` // text_delta, input_json_delta, thinking_delta
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

// Decode reads stream-json lines from r and calls fn for each event. Lines
// that are not JSON objects are skipped.
func Decode(r io.Reader, fn func(Event) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return fmt.Errorf("decode stream-json line: %w", err)
		}
		if err := fn(ev); err != nil {
			return err
		}
	}
	return sc.Err()
}
