package citation

import (
	"regexp"
	"sort"
	"strings"
)

var fenceRe = regexp.MustCompile("^([ \t]*)(`{3,}|~{3,})(.*)$")

// codeFenceTracker tracks fenced code blocks in streamed markdown so citations
// inside code are left alone. It is a simplified port of onyx's
// CodeFenceTracker: it honours fence character and length and ignores inline
// backticks, but does not model blockquote or list containers.
type codeFenceTracker struct {
	open        string // the opening fence while inside a block
	partialLine string
	lineStart   int
	// transitions holds byte offsets of lines that open or close a block.
	transitions []int
}

func (t *codeFenceTracker) feed(text string) {
	lines := strings.Split(t.partialLine+text, "\n")
	t.partialLine = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		t.processLine(line)
		t.lineStart += len(line) + 1
	}
}

// flush finishes the pending line and closes any open block.
func (t *codeFenceTracker) flush() {
	t.processLine(t.partialLine)
	t.lineStart += len(t.partialLine)
	t.partialLine = ""
	if t.open != "" {
		t.setOpen("")
	}
}

func (t *codeFenceTracker) inCodeBlockAt(offset int) bool {
	return sort.SearchInts(t.transitions, offset+1)%2 == 1
}

func (t *codeFenceTracker) processLine(line string) {
	m := fenceRe.FindStringSubmatch(line)
	if m == nil || len(strings.ReplaceAll(m[1], "\t", "    ")) > 3 {
		return
	}
	fence, info := m[2], m[3]
	if t.open != "" {
		if fence[0] == t.open[0] && len(fence) >= len(t.open) && strings.TrimSpace(info) == "" {
			t.setOpen("")
		}
		return
	}
	// Backtick fences cannot have backticks in the info string.
	if fence[0] == '`' && strings.Contains(info, "`") {
		return
	}
	t.setOpen(fence)
}

func (t *codeFenceTracker) setOpen(fence string) {
	t.open = fence
	t.transitions = append(t.transitions, t.lineStart)
}
