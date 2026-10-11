// Package protocol holds the types shared between the agent, the channels and
// the Hatchet workflows. Packets mirror onyx's streaming packet model
// (backend/onyx/server/query_and_chat/streaming_models.py), trimmed to what a
// claude -p driven turn can produce.
package protocol

// PacketType identifies the kind of a Packet.
type PacketType string

const (
	PacketMessageDelta PacketType = "message_delta"
	PacketCitation     PacketType = "citation_info"
	PacketToolStart    PacketType = "tool_start"
	PacketToolResult   PacketType = "tool_result"
	PacketStop         PacketType = "stop"
	PacketError        PacketType = "error"
)

// Packet is one streaming update from an agent turn. Only the fields relevant
// to Type are set.
type Packet struct {
	Type PacketType `json:"type"`

	// message_delta
	Content string `json:"content,omitempty"`

	// citation_info
	CitationNumber int        `json:"citation_number,omitempty"`
	Document       *SearchDoc `json:"document,omitempty"`

	// tool_start / tool_result
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolArgs   string `json:"tool_args,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`

	// stop / error
	StopReason string `json:"stop_reason,omitempty"`
	Error      string `json:"error,omitempty"`
}

// SearchDoc is a citeable document handed to the LLM by a tool.
type SearchDoc struct {
	DocumentID string `json:"document_id"`
	Title      string `json:"title"`
	URL        string `json:"url,omitempty"`
	SourceType string `json:"source_type,omitempty"`
}
