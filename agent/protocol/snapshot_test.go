package protocol

import "testing"

func TestSnapshotApply(t *testing.T) {
	var s Snapshot
	for _, p := range []Packet{
		{Type: PacketToolStart, ToolCallID: "t1", ToolName: "memory_search"},
		{Type: PacketToolStart, ToolCallID: "t1", ToolName: "memory_search"},
		{Type: PacketToolResult, ToolCallID: "t1"},
		{Type: PacketMessageDelta, Content: "hello "},
		{Type: PacketCitation, CitationNumber: 2, Document: &SearchDoc{DocumentID: "b"}},
		{Type: PacketCitation, CitationNumber: 1, Document: &SearchDoc{DocumentID: "a"}},
		{Type: PacketMessageDelta, Content: "world"},
		{Type: PacketStop},
	} {
		s.Apply(p)
	}
	if s.Text != "hello world" {
		t.Fatalf("text = %q", s.Text)
	}
	if len(s.Tools) != 1 || !s.Tools[0].Done {
		t.Fatalf("tools = %+v", s.Tools)
	}
	if got := s.CitationNumbers(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("citations = %v", got)
	}
	if !s.Done {
		t.Fatal("not done")
	}
}
