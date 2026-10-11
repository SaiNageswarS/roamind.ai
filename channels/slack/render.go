package slack

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/slack-go/slack"

	"roamind.ai/agent/protocol"
)

// MaxChunk bounds each Slack message so it also fits a section block (3000).
const MaxChunk = 2900

var (
	mdLinkRe     = regexp.MustCompile(`\[([^\[\]]*(?:\[[^\[\]]*\][^\[\]]*)*)\]\(([^)\s]+)\)`)
	boldRe       = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	italicStarRe = regexp.MustCompile(`(^|[^*\w])\*([^*\s][^*]*?)\*([^*\w]|$)`)
	strikeRe     = regexp.MustCompile(`~~(.+?)~~`)
	headingRe    = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
)

// ToMrkdwn converts common Markdown to Slack mrkdwn. Fenced and inline code
// are passed through untouched.
func ToMrkdwn(md string) string {
	var out strings.Builder
	inFence := false
	for i, line := range strings.Split(md, "\n") {
		if i > 0 {
			out.WriteByte('\n')
		}
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			out.WriteString(line)
			continue
		}
		if inFence {
			out.WriteString(line)
			continue
		}
		out.WriteString(convertLine(line))
	}
	return out.String()
}

func convertLine(line string) string {
	// Split on inline code spans; only convert the parts outside them.
	parts := strings.Split(line, "`")
	for i := 0; i < len(parts); i += 2 {
		parts[i] = convertProse(parts[i])
	}
	return strings.Join(parts, "`")
}

func convertProse(s string) string {
	// Bold and headings become \x01-delimited first so the italic pass does
	// not mistake Slack's *bold* for Markdown *italic*.
	s = escape(s)
	s = headingRe.ReplaceAllString(s, "\x01$1\x01")
	s = boldRe.ReplaceAllStringFunc(s, func(m string) string {
		return "\x01" + m[2:len(m)-2] + "\x01"
	})
	s = italicStarRe.ReplaceAllString(s, "${1}_${2}_${3}")
	s = strings.ReplaceAll(s, "\x01", "*")
	s = strikeRe.ReplaceAllString(s, "~$1~")
	s = mdLinkRe.ReplaceAllString(s, "<$2|$1>")
	return s
}

// escape encodes the three characters Slack treats as control sequences.
func escape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}

// RenderBody renders a snapshot's tool lines and answer text as mrkdwn.
func RenderBody(s protocol.Snapshot) string {
	var b strings.Builder
	for _, t := range s.Tools {
		icon := "🔧"
		suffix := "…"
		if t.Done {
			icon, suffix = "✓", ""
			if t.IsError {
				icon = "✗"
			}
		}
		fmt.Fprintf(&b, "_%s %s%s_\n", icon, toolDisplayName(t.Name), suffix)
	}
	if len(s.Tools) > 0 && s.Text != "" {
		b.WriteByte('\n')
	}
	b.WriteString(ToMrkdwn(s.Text))
	if s.Error != "" {
		fmt.Fprintf(&b, "\n\n:warning: _Something went wrong: %s_", escape(s.Error))
	}
	return strings.TrimSpace(b.String())
}

// toolDisplayName shortens mcp__server__tool to tool.
func toolDisplayName(name string) string {
	if strings.HasPrefix(name, "mcp__") {
		if i := strings.LastIndex(name, "__"); i > 3 {
			return name[i+2:]
		}
	}
	return name
}

// SourcesBlocks renders cited documents like onyx's _build_sources_blocks.
func SourcesBlocks(s protocol.Snapshot) []slack.Block {
	nums := s.CitationNumbers()
	if len(nums) == 0 {
		return nil
	}
	blocks := []slack.Block{
		slack.NewDividerBlock(),
		slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, "*Sources:*", false, false), nil, nil),
	}
	for _, n := range nums {
		d := s.Citations[n]
		title := d.Title
		if title == "" {
			title = d.DocumentID
		}
		label := fmt.Sprintf("[%d] %s", n, escape(title))
		if d.URL != "" {
			label = fmt.Sprintf("<%s|%s>", d.URL, label)
		}
		blocks = append(blocks, slack.NewContextBlock("", slack.NewTextBlockObject(slack.MarkdownType, "*"+label+"*", false, false)))
		if len(blocks) >= 48 { // Slack allows 50 blocks per message.
			break
		}
	}
	return blocks
}

// Split breaks text into chunks of at most max bytes, preferring paragraph,
// then line, then word boundaries. It does not split inside a code fence when
// it can avoid it.
func Split(text string, max int) []string {
	var chunks []string
	for len(text) > max {
		cut := lastBoundary(text[:max])
		chunks = append(chunks, strings.TrimRight(text[:cut], " \n"))
		text = strings.TrimLeft(text[cut:], " \n")
	}
	if text = strings.TrimRight(text, " \n"); text != "" || len(chunks) == 0 {
		chunks = append(chunks, text)
	}
	return chunks
}

func lastBoundary(s string) int {
	for _, sep := range []string{"\n\n", "\n", " "} {
		if i := strings.LastIndex(s, sep); i > len(s)/2 {
			return i + len(sep)
		}
	}
	// No good boundary: cut at a rune boundary.
	i := len(s)
	for i > 0 && (s[i-1]&0xC0) == 0x80 {
		i--
	}
	if i > 0 && s[i-1] >= 0xC0 {
		i--
	}
	if i == 0 {
		return len(s)
	}
	return i
}
