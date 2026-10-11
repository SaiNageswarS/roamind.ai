package citation

import (
	"strings"
	"testing"

	"roamind.ai/agent/protocol"
)

func docs() map[int]protocol.SearchDoc {
	return map[int]protocol.SearchDoc{
		1: {DocumentID: "a", Title: "A", URL: "https://a"},
		2: {DocumentID: "b", Title: "B", URL: "https://b"},
	}
}

func run(t *testing.T, mode Mode, tokens ...string) (string, []int) {
	t.Helper()
	p := New(mode)
	p.UpdateMapping(docs())
	var sb strings.Builder
	var cites []int
	collect := func(ps []protocol.Packet) {
		for _, pk := range ps {
			switch pk.Type {
			case protocol.PacketMessageDelta:
				sb.WriteString(pk.Content)
			case protocol.PacketCitation:
				cites = append(cites, pk.CitationNumber)
			}
		}
	}
	for _, tok := range tokens {
		collect(p.Process(tok))
	}
	collect(p.Flush())
	return sb.String(), cites
}

func TestHyperlinkSplitTokens(t *testing.T) {
	got, cites := run(t, Hyperlink, "Paris is the capital ", "[", "1", "]", " of France.")
	if want := "Paris is the capital [[1]](https://a) of France."; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if len(cites) != 1 || cites[0] != 1 {
		t.Fatalf("cites = %v", cites)
	}
}

func TestHyperlinkMulti(t *testing.T) {
	got, cites := run(t, Hyperlink, "Facts [1, 2].")
	if want := "Facts [[1]](https://a) [[2]](https://b)."; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if len(cites) != 2 {
		t.Fatalf("cites = %v", cites)
	}
}

func TestNoLeadingSpaceAddsOne(t *testing.T) {
	got, _ := run(t, Hyperlink, "Fact[2].")
	if want := "Fact [[2]](https://b)."; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDoubleBracketAndUnicode(t *testing.T) {
	got, _ := run(t, Hyperlink, "x [[1]] y 【2】")
	if want := "x [[1]](https://a) y [[2]](https://b)"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestUnknownNumberKept(t *testing.T) {
	got, cites := run(t, Hyperlink, "x [9] y")
	if got != "x [9] y" || len(cites) != 0 {
		t.Fatalf("got %q cites %v", got, cites)
	}
}

func TestCodeFenceIgnored(t *testing.T) {
	got, cites := run(t, Hyperlink, "```\narr[1]\n```\nok [1]")
	if want := "```\narr[1]\n```\nok [[1]](https://a)"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if len(cites) != 1 {
		t.Fatalf("cites = %v", cites)
	}
}

func TestPlainBracketTextFlushed(t *testing.T) {
	got, _ := run(t, Hyperlink, "array [", "x]")
	if got != "array [x]" {
		t.Fatalf("got %q", got)
	}
	got, _ = run(t, Hyperlink, "ends with [")
	if got != "ends with [" {
		t.Fatalf("flush got %q", got)
	}
}

func TestKeepMarkers(t *testing.T) {
	got, cites := run(t, KeepMarkers, "a [1] b")
	if got != "a [1] b" || len(cites) != 0 {
		t.Fatalf("got %q cites %v", got, cites)
	}
}

func TestRemove(t *testing.T) {
	got, _ := run(t, Remove, "a [1]. b")
	if got != "a. b" {
		t.Fatalf("got %q", got)
	}
}

func TestRepeatCitationEmitsOnce(t *testing.T) {
	_, cites := run(t, Hyperlink, "a [1] long gap text here [1]")
	if len(cites) != 1 {
		t.Fatalf("cites = %v", cites)
	}
}

func TestNextCitationNumber(t *testing.T) {
	p := New(Hyperlink)
	if p.NextCitationNumber() != 1 {
		t.Fatal("want 1")
	}
	p.UpdateMapping(docs())
	if p.NextCitationNumber() != 3 {
		t.Fatal("want 3")
	}
}
