package protocol

import "sort"

// ToolLine is the display state of one tool call within a turn.
type ToolLine struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Done    bool   `json:"done"`
	IsError bool   `json:"is_error"`
}

// Snapshot is the cumulative state of a turn, built by applying packets in
// order. The Slack updater renders snapshots rather than individual packets so a
// renderer can always redraw the whole message.
type Snapshot struct {
	Text      string            `json:"text"`
	Tools     []ToolLine        `json:"tools,omitempty"`
	Citations map[int]SearchDoc `json:"citations,omitempty"`
	Done      bool              `json:"done"`
	Error     string            `json:"error,omitempty"`
}

// Apply folds a packet into the snapshot.
func (s *Snapshot) Apply(p Packet) {
	switch p.Type {
	case PacketMessageDelta:
		s.Text += p.Content
	case PacketCitation:
		if p.Document == nil {
			return
		}
		if s.Citations == nil {
			s.Citations = map[int]SearchDoc{}
		}
		s.Citations[p.CitationNumber] = *p.Document
	case PacketToolStart:
		for _, t := range s.Tools {
			if t.ID == p.ToolCallID {
				return
			}
		}
		s.Tools = append(s.Tools, ToolLine{ID: p.ToolCallID, Name: p.ToolName})
	case PacketToolResult:
		for i := range s.Tools {
			if s.Tools[i].ID == p.ToolCallID {
				s.Tools[i].Done = true
				s.Tools[i].IsError = p.IsError
			}
		}
	case PacketStop:
		s.Done = true
	case PacketError:
		s.Done = true
		s.Error = p.Error
	}
}

// CitationNumbers returns the cited numbers in ascending order.
func (s *Snapshot) CitationNumbers() []int {
	nums := make([]int, 0, len(s.Citations))
	for n := range s.Citations {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	return nums
}
