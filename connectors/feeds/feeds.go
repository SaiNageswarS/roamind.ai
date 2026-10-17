// Package feeds reads the RSS and Atom feeds that are the sources of news
// interests.
package feeds

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"roamind.ai/connectors/safehttp"
)

// Entry is one item of a feed.
type Entry struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	// Content is the entry's summary as plain text, cut to MaxContent.
	Content   string    `json:"content"`
	Published time.Time `json:"published,omitempty"` // zero when the feed has no date
	Feed      string    `json:"feed"`                // the feed's title
}

// MaxContent is how much of an entry's summary is kept.
const MaxContent = 600

// MaxBody caps a feed's size; the largest curated feeds are about 1 MB.
const MaxBody = 8 << 20

// Client fetches feeds.
type Client struct {
	HTTP *http.Client
}

// New returns a client that gives up on a feed after timeout and connects
// only to public addresses: feed URLs can come from the agent.
func New(timeout time.Duration) *Client {
	return &Client{HTTP: safehttp.New(timeout)}
}

// Fetch reads the feed at url and returns its entries with a title and a
// link, newest first.
func (c *Client) Fetch(ctx context.Context, url string) ([]Entry, error) {
	f, err := c.parse(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("feed %s: %w", url, err)
	}
	var out []Entry
	for _, it := range f.Items {
		link := strings.TrimSpace(it.Link)
		title := clean(it.Title, 300)
		if link == "" || title == "" {
			continue
		}
		e := Entry{Title: title, URL: link, Feed: clean(f.Title, 200)}
		if e.Content = clean(it.Description, MaxContent); e.Content == "" {
			e.Content = clean(it.Content, MaxContent)
		}
		if it.PublishedParsed != nil {
			e.Published = *it.PublishedParsed
		} else if it.UpdatedParsed != nil {
			e.Published = *it.UpdatedParsed
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Published.After(out[j].Published) })
	return out, nil
}

func (c *Client) parse(ctx context.Context, url string) (*gofeed.Feed, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Roamind news shorts)")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %s", resp.Status)
	}
	body, err := safehttp.ReadAll(resp.Body, MaxBody)
	if err != nil {
		return nil, err
	}
	return gofeed.NewParser().Parse(bytes.NewReader(body))
}

var (
	tagRe   = regexp.MustCompile(`<[^>]*>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// clean turns feed HTML into one line of plain text of at most n bytes,
// cut at a word.
func clean(s string, n int) string {
	s = strings.TrimSpace(spaceRe.ReplaceAllString(html.UnescapeString(tagRe.ReplaceAllString(s, " ")), " "))
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}
