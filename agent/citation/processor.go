// Package citation is a Go port of onyx's DynamicCitationProcessor
// (backend/onyx/chat/citation_processor.py). It consumes streamed LLM text,
// holds back possible partial citations such as "[" or "[1", and rewrites
// complete citations ([1], [1, 2], [[1]], 【1】) according to the mode.
package citation

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"roamind.ai/agent/protocol"
)

// Mode controls how citations appear in the output text.
type Mode int

const (
	// Hyperlink rewrites [1] to [[1]](url) and emits citation packets.
	Hyperlink Mode = iota
	// KeepMarkers leaves [1] untouched and emits no citation packets.
	KeepMarkers
	// Remove strips citation markers from the text.
	Remove
)

var (
	// Possible incomplete citation at the end of the segment: '[', '[[', '[1', '[1,', '[1, '.
	possibleCitationRe = regexp.MustCompile(`[\[【［]+(?:\d+(?:, ?\d+)*(?:, ?)?)?$`)
	// Group 1: [[1]] (already formatted). Group 2: [1], [1, 2], [1,2,16].
	citationRe = regexp.MustCompile(`([\[【［]{2}\d+[\]】］]{2})|([\[【［]\d+(?:, ?\d+)*[\]】］])`)
)

// Processor turns a stream of text tokens into message_delta and
// citation_info packets. It is not safe for concurrent use.
type Processor struct {
	mode          Mode
	citationToDoc map[int]protocol.SearchDoc
	seen          map[int]protocol.SearchDoc

	llmOut      strings.Builder
	fences      codeFenceTracker
	currSegment string

	citedInOrder     []protocol.SearchDoc
	citedIDs         map[string]bool
	recentCited      map[string]bool
	nonCitationCount int
}

// New returns a processor in the given mode.
func New(mode Mode) *Processor {
	return &Processor{
		mode:          mode,
		citationToDoc: map[int]protocol.SearchDoc{},
		seen:          map[int]protocol.SearchDoc{},
		citedIDs:      map[string]bool{},
		recentCited:   map[string]bool{},
	}
}

// UpdateMapping merges citation number → document mappings. Existing numbers
// are kept, matching onyx's default (update_duplicate_keys=False).
func (p *Processor) UpdateMapping(m map[int]protocol.SearchDoc) {
	for n, d := range m {
		if _, ok := p.citationToDoc[n]; !ok {
			p.citationToDoc[n] = d
		}
	}
}

// NextCitationNumber is the number a tool should give its first new document.
func (p *Processor) NextCitationNumber() int {
	next := 1
	for n := range p.citationToDoc {
		if n >= next {
			next = n + 1
		}
	}
	return next
}

// Seen returns every citation that appeared in the text, regardless of mode.
func (p *Processor) Seen() map[int]protocol.SearchDoc { return p.seen }

// CitedDocuments returns documents in the order they were first cited
// (Hyperlink mode only).
func (p *Processor) CitedDocuments() []protocol.SearchDoc { return p.citedInOrder }

// Process consumes one token and returns the packets that are safe to emit.
func (p *Processor) Process(token string) []protocol.Packet {
	var out []protocol.Packet
	text := func(s string) {
		if s != "" {
			out = append(out, protocol.Packet{Type: protocol.PacketMessageDelta, Content: s})
		}
	}

	p.currSegment += token
	p.llmOut.WriteString(token)
	p.fences.feed(token)
	llmOut := p.llmOut.String()
	segmentOffset := len(llmOut) - len(p.currSegment)

	var matches [][]int
	for _, m := range citationRe.FindAllStringSubmatchIndex(p.currSegment, -1) {
		if !p.fences.inCodeBlockAt(segmentOffset + m[0]) {
			matches = append(matches, m)
		}
	}
	possible := possibleCitationRe.MatchString(p.currSegment)

	if len(matches) > 0 {
		idx := 0
		for _, m := range matches {
			inter := p.currSegment[idx:m[0]]
			p.nonCitationCount += len(inter)
			idx = m[1]

			var leadingSpace bool
			switch {
			case inter != "":
				r, _ := utf8.DecodeLastRuneInString(inter)
				leadingSpace = unicode.IsSpace(r)
			case idx > 0 && m[0] > 0:
				leadingSpace = true
			case segmentOffset > 0:
				r, _ := utf8.DecodeLastRuneInString(llmOut[:segmentOffset])
				leadingSpace = unicode.IsSpace(r)
			}

			if p.nonCitationCount > 5 {
				clear(p.recentCited)
			}
			citationText, infos := p.processCitation(p.currSegment[m[0]:m[1]], m[2] >= 0, leadingSpace)

			switch p.mode {
			case Hyperlink:
				text(inter)
				out = append(out, infos...)
				text(citationText)
			case KeepMarkers:
				text(inter)
				text(p.currSegment[m[0]:m[1]])
			case Remove:
				rest := p.currSegment[m[1]:]
				if inter != "" && rest != "" {
					last, _ := utf8.DecodeLastRuneInString(inter)
					first, _ := utf8.DecodeRuneInString(rest)
					if unicode.IsSpace(last) && (unicode.IsSpace(first) || strings.ContainsRune(".,;:!?)]}", first)) {
						inter = strings.TrimRightFunc(inter, unicode.IsSpace)
					}
				}
				text(inter)
			}
			p.nonCitationCount = 0
		}
		p.currSegment = p.currSegment[idx:]
		p.nonCitationCount = len(p.currSegment)
		possible = possibleCitationRe.MatchString(p.currSegment)
	}

	if !possible {
		text(p.currSegment)
		p.nonCitationCount += len(p.currSegment)
		p.currSegment = ""
	}
	return out
}

// Flush emits any held-back text at the end of an LLM step.
func (p *Processor) Flush() []protocol.Packet {
	p.fences.flush()
	if p.currSegment == "" {
		return nil
	}
	s := p.currSegment
	p.currSegment = ""
	return []protocol.Packet{{Type: protocol.PacketMessageDelta, Content: s}}
}

func (p *Processor) processCitation(match string, formatted, leadingSpace bool) (string, []protocol.Packet) {
	// Strip one or two brackets on each side; brackets may be multi-byte.
	inner := match
	n := 1
	if formatted {
		n = 2
	}
	for range n {
		_, sz := utf8.DecodeRuneInString(inner)
		inner = inner[sz:]
		_, sz = utf8.DecodeLastRuneInString(inner)
		inner = inner[:len(inner)-sz]
	}

	var parts []string
	var infos []protocol.Packet
	resolved := false
	for _, s := range strings.Split(inner, ",") {
		num, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			continue
		}
		doc, ok := p.citationToDoc[num]
		if !ok {
			continue
		}
		resolved = true
		p.seen[num] = doc
		if p.mode != Hyperlink {
			continue
		}
		parts = append(parts, fmt.Sprintf("[[%d]](%s)", num, doc.URL))
		if p.recentCited[doc.DocumentID] {
			continue
		}
		p.recentCited[doc.DocumentID] = true
		if !p.citedIDs[doc.DocumentID] {
			p.citedIDs[doc.DocumentID] = true
			p.citedInOrder = append(p.citedInOrder, doc)
			d := doc
			infos = append(infos, protocol.Packet{Type: protocol.PacketCitation, CitationNumber: num, Document: &d})
		}
	}
	// Unlike onyx, a marker whose numbers all fail to resolve is kept verbatim:
	// with no citeable tool in the turn, "[2]" is far more likely to be
	// ordinary text (a list index, a footnote) than a hallucinated citation.
	if !resolved && p.mode == Hyperlink {
		return match, nil
	}
	formattedText := strings.Join(parts, " ")
	if formattedText != "" && !leadingSpace {
		formattedText = " " + formattedText
	}
	return formattedText, infos
}
