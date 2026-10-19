// Package jobs reads job postings from applicant tracking systems: through
// the public APIs of Greenhouse, Lever, Ashby and Workday, and of
// SmartRecruiters, Recruitee, Personio, Workable and Oracle Recruiting
// Cloud, common in Europe and Hong Kong. Any other page is read from its
// JobPosting data when it has some, else rendered in a browser and read
// as text.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"roamind.ai/connectors/safehttp"
)

// Sources of a posting.
const (
	Greenhouse = "greenhouse"
	Lever      = "lever"
	Ashby      = "ashby"
	Workday    = "workday"
	// SmartRecruiters, Recruitee, Personio, Workable and Oracle Recruiting
	// Cloud: common in Europe and Hong Kong.
	SmartRecruiters = "smartrecruiters"
	Recruitee       = "recruitee"
	Personio        = "personio"
	Workable        = "workable"
	Oracle          = "oracle"
	Other           = "other"
)

// MaxDescription is how much of a posting's description is kept.
const MaxDescription = 12000

// MaxBody caps a response.
const MaxBody = 8 << 20

// ErrGone is returned for a posting its site no longer has.
var ErrGone = errors.New("the posting is gone")

// ErrBlocked is returned for URLs on the client's blocked domains.
var ErrBlocked = errors.New("the site is blocked")

// ErrNoText is returned for a page with no posting text, even rendered.
var ErrNoText = errors.New("the page has no posting text")

// Posting is a job posting.
type Posting struct {
	URL      string `json:"url"` // canonical, see Parse
	ApplyURL string `json:"apply_url,omitempty"`
	Source   string `json:"source"`
	Title    string `json:"title"`
	Company  string `json:"company,omitempty"`
	Location string `json:"location,omitempty"`
	Posted   string `json:"posted,omitempty"` // ISO date
	// Description is plain text, cut to MaxDescription.
	Description string `json:"description"`
	// Questions are the application form's, when the site publishes them
	// (Greenhouse).
	Questions []Question `json:"questions,omitempty"`
}

// Question is one question of an application form.
type Question struct {
	Label    string   `json:"label"`
	Required bool     `json:"required"`
	Kind     string   `json:"kind"` // text, textarea, select, multiselect or file
	Options  []string `json:"options,omitempty"`
}

// Ref is a posting URL resolved to its site.
type Ref struct {
	Source string
	// URL is the posting's canonical URL: its site's own address for it,
	// without tracking parameters or the form's path, so the same posting
	// found twice has the same URL.
	URL string
	// ats is the system the posting is on, nil for any other page; host,
	// board and id address the posting in its API: the Greenhouse board,
	// Lever site, Ashby organization, Workday tenant and site, and so on.
	ats             ATS
	host, board, id string
}

// Parse resolves a posting URL to its site: the first of systems to match
// it, else a page elsewhere.
func Parse(raw string) (Ref, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return Ref{}, fmt.Errorf("not a web address: %q", raw)
	}
	for _, s := range systems {
		if ref, ok := s.Match(u); ok {
			ref.ats = s
			return ref, nil
		}
	}
	return Ref{Source: Other, URL: withoutFragment(u)}, nil
}

// withoutFragment is u without its #fragment.
func withoutFragment(u *url.URL) string {
	v := *u
	v.Fragment, v.RawFragment = "", ""
	return v.String()
}

// Client fetches postings.
type Client struct {
	HTTP *http.Client
	// Blocked are domains never read, with their subdomains.
	Blocked []string
	// Render, if set, renders every page not read through an API, unless
	// its HTML carries the posting's JobPosting data.
	Render Renderer
}

// New returns a client that gives up on a request after timeout and
// connects only to public addresses: posting URLs come from web searches.
func New(timeout time.Duration) *Client {
	return &Client{HTTP: safehttp.New(timeout)}
}

// Blocks reports whether raw is on one of the client's blocked domains or
// their subdomains.
func (c *Client) Blocks(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, d := range c.Blocked {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
			return true
		}
	}
	return false
}

// Fetch reads the posting at raw: through the API of the system Parse
// matched it to, else from its page. It returns ErrGone when the site no
// longer has the posting.
func (c *Client) Fetch(ctx context.Context, raw string) (*Posting, error) {
	ref, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if c.Blocks(ref.URL) {
		return nil, fmt.Errorf("posting %s: %w", ref.URL, ErrBlocked)
	}
	var p *Posting
	if ref.ats != nil {
		p, err = ref.ats.Posting(ctx, c, ref)
	}
	if ref.ats == nil || errors.Is(err, errNotHere) {
		ref.Source = Other
		p, err = c.page(ctx, ref)
	}
	if err == nil && strings.TrimSpace(p.Description) == "" {
		err = ErrNoText
	}
	if err != nil {
		return nil, fmt.Errorf("posting %s: %w", ref.URL, err)
	}
	if p.Source == "" {
		p.Source = ref.Source
	}
	p.Description = cut(p.Description, MaxDescription)
	if p.URL == "" {
		p.URL = ref.URL
	}
	return p, nil
}

// page reads a posting from its page: from the JobPosting data its HTML
// carries, else from its text once rendered, so whatever its scripts fill
// in is there. Without a renderer, or when rendering fails, it reads the
// HTML as sent. A page the site says is gone isn't rendered.
func (c *Client) page(ctx context.Context, ref Ref) (*Posting, error) {
	plain, err := c.plainPage(ctx, ref)
	switch {
	case errors.Is(err, ErrGone):
		return nil, err
	case err == nil && plain.structured:
		return &plain.Posting, nil
	}
	if c.Render != nil {
		if doc, rerr := c.Render.Render(ctx, ref.URL); rerr == nil {
			if r := pageOf(ref, doc); r.Description != "" {
				return &r.Posting, nil
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return &plain.Posting, nil
}

// plainPage reads a page's HTML as sent, without running its scripts.
func (c *Client) plainPage(ctx context.Context, ref Ref) (*pagePosting, error) {
	resp, err := c.get(ctx, ref.URL, "text/html")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := safehttp.ReadAll(resp.Body, MaxBody)
	if err != nil {
		return nil, err
	}
	return pageOf(ref, string(body)), nil
}

// pagePosting is a posting read from a page; structured when it came from
// the page's JobPosting data rather than its text.
type pagePosting struct {
	Posting
	structured bool
}

// pageOf is the posting in a page's HTML: its JobPosting data, else its
// text under the page's title.
func pageOf(ref Ref, page string) *pagePosting {
	if p := ldPosting(page); p != nil {
		p.ApplyURL = ref.URL
		return &pagePosting{Posting: *p, structured: true}
	}
	var title string
	if m := titleRe.FindStringSubmatch(page); m != nil {
		title = strings.TrimSpace(html.UnescapeString(m[1]))
	}
	return &pagePosting{Posting: Posting{ApplyURL: ref.URL, Title: title, Description: Text(page)}}
}

func (c *Client) getJSON(ctx context.Context, u string, v any) error {
	resp, err := c.get(ctx, u, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := safehttp.ReadAll(resp.Body, MaxBody)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// get requests u, mapping a missing posting to ErrGone.
func (c *Client) get(ctx context.Context, u, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Roamind job assistant)")
	req.Header.Set("Accept", accept)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		resp.Body.Close()
		return nil, ErrGone
	case resp.StatusCode != http.StatusOK:
		resp.Body.Close()
		return nil, fmt.Errorf("http %s", resp.Status)
	}
	return resp, nil
}

var (
	titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	dropRe  = regexp.MustCompile(`(?is)<(script|style|noscript|svg|head)\b.*?</(script|style|noscript|svg|head)>`)
	breakRe = regexp.MustCompile(`(?i)<(br|/p|/div|/h[1-6]|/tr|/ul|/ol)\b[^>]*>`)
	itemRe  = regexp.MustCompile(`(?i)<li\b[^>]*>`)
	tagRe   = regexp.MustCompile(`<[^>]*>`)
	spaceRe = regexp.MustCompile(`[ \t\r\f\v\x{00a0}]+`)
	blankRe = regexp.MustCompile(`\n{3,}`)
)

// Text turns HTML into plain text: one line per block, list items as
// "- ", without scripts and styles.
func Text(s string) string {
	s = dropRe.ReplaceAllString(s, "")
	s = itemRe.ReplaceAllString(s, "\n- ")
	s = breakRe.ReplaceAllString(s, "\n")
	s = html.UnescapeString(tagRe.ReplaceAllString(s, ""))
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(spaceRe.ReplaceAllString(l, " "))
	}
	return strings.TrimSpace(blankRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// isoDate is the date of an RFC 3339 timestamp or ISO date, or "".
func isoDate(s string) string {
	if len(s) >= 10 {
		if _, err := time.Parse(time.DateOnly, s[:10]); err == nil {
			return s[:10]
		}
	}
	return ""
}

// cut shortens s to at most n bytes, at a line or word.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	c := s[:n]
	if i := strings.LastIndexAny(c, "\n "); i > n/2 {
		c = c[:i]
	}
	return c + "…"
}
