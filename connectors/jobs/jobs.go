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
	"io"
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

// MaxBoard caps an Ashby board, which lists every job of a company with
// its description; it is read as a stream, so it isn't held in memory.
const MaxBoard = 64 << 20

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
	// host, board and id address the posting in its site's API: the
	// Greenhouse board, Lever site, Ashby organization or Workday tenant
	// and site.
	host, board, id string
}

var (
	ghPath = regexp.MustCompile(`^/([^/]+)/jobs/(\d+)`)
	idPath = regexp.MustCompile(`^/([^/]+)/([0-9a-f-]{36})`)
	wdPath = regexp.MustCompile(`^/(?:[a-z]{2}-[A-Z]{2}/)?([^/]+)(/job/.+)$`)
	digits = regexp.MustCompile(`^\d+$`)
	srPath = regexp.MustCompile(`^/([^/]+)/(\d+)`)
	rtPath = regexp.MustCompile(`^/o/([^/]+)`)
	pePath = regexp.MustCompile(`^/job/(\d+)`)
	peHost = regexp.MustCompile(`^[a-z0-9-]+\.jobs\.personio\.(de|com)$`)
	wkPath = regexp.MustCompile(`^/([^/]+)/j/([0-9A-Za-z]+)`)
	orPath = regexp.MustCompile(`^/hcmUI/CandidateExperience/[^/]+/sites/([^/]+)/job/(\d+)`)
)

// Parse resolves a posting URL to its site.
func Parse(raw string) (Ref, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return Ref{}, fmt.Errorf("not a web address: %q", raw)
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.HasSuffix(host, "greenhouse.io"):
		if m := ghPath.FindStringSubmatch(u.Path); m != nil {
			api := "boards-api.greenhouse.io"
			if strings.Contains(host, ".eu.") {
				api = "boards-api.eu.greenhouse.io"
			}
			return Ref{Source: Greenhouse, URL: "https://" + host + "/" + m[1] + "/jobs/" + m[2], host: api, board: m[1], id: m[2]}, nil
		}
		if q := u.Query(); u.Path == "/embed/job_app" && q.Get("for") != "" && digits.MatchString(q.Get("token")) {
			return Ref{Source: Greenhouse, URL: "https://job-boards.greenhouse.io/" + q.Get("for") + "/jobs/" + q.Get("token"),
				host: "boards-api.greenhouse.io", board: q.Get("for"), id: q.Get("token")}, nil
		}
	case host == "jobs.lever.co" || host == "jobs.eu.lever.co":
		if m := idPath.FindStringSubmatch(u.Path); m != nil {
			api := "api.lever.co"
			if host == "jobs.eu.lever.co" {
				api = "api.eu.lever.co"
			}
			return Ref{Source: Lever, URL: "https://" + host + "/" + m[1] + "/" + m[2], host: api, board: m[1], id: m[2]}, nil
		}
	case host == "jobs.ashbyhq.com":
		if m := idPath.FindStringSubmatch(u.Path); m != nil {
			return Ref{Source: Ashby, URL: "https://jobs.ashbyhq.com/" + m[1] + "/" + m[2], host: "api.ashbyhq.com", board: m[1], id: m[2]}, nil
		}
	case host == "jobs.smartrecruiters.com" || host == "careers.smartrecruiters.com":
		if m := srPath.FindStringSubmatch(u.Path); m != nil {
			return Ref{Source: SmartRecruiters, URL: "https://jobs.smartrecruiters.com/" + m[1] + "/" + m[2], host: "api.smartrecruiters.com", board: m[1], id: m[2]}, nil
		}
	case strings.HasSuffix(host, ".recruitee.com"):
		if m := rtPath.FindStringSubmatch(u.Path); m != nil {
			company, _, _ := strings.Cut(host, ".")
			return Ref{Source: Recruitee, URL: "https://" + host + "/o/" + m[1], host: host, board: company, id: m[1]}, nil
		}
	case peHost.MatchString(host):
		if m := pePath.FindStringSubmatch(u.Path); m != nil {
			company, _, _ := strings.Cut(host, ".")
			return Ref{Source: Personio, URL: "https://" + host + "/job/" + m[1], host: host, board: company, id: m[1]}, nil
		}
	case host == "apply.workable.com":
		if m := wkPath.FindStringSubmatch(u.Path); m != nil {
			return Ref{Source: Workable, URL: "https://apply.workable.com/" + m[1] + "/j/" + m[2], host: host, board: m[1], id: m[2]}, nil
		}
	case strings.HasSuffix(host, ".oraclecloud.com"):
		if m := orPath.FindStringSubmatch(u.Path); m != nil {
			return Ref{Source: Oracle, URL: "https://" + host + "/hcmUI/CandidateExperience/en/sites/" + m[1] + "/job/" + m[2], host: host, board: m[1], id: m[2]}, nil
		}
	case strings.HasSuffix(host, ".myworkdayjobs.com"):
		if m := wdPath.FindStringSubmatch(u.Path); m != nil {
			tenant, _, _ := strings.Cut(host, ".")
			return Ref{Source: Workday, URL: "https://" + host + "/" + m[1] + m[2], host: host, board: tenant + "/" + m[1], id: m[2]}, nil
		}
	}
	u.Fragment, u.RawFragment = "", ""
	return Ref{Source: Other, URL: u.String()}, nil
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

// Fetch reads the posting at raw: from its site's API when it has one,
// else as the text of its page. It returns ErrGone when the site no longer
// has the posting.
func (c *Client) Fetch(ctx context.Context, raw string) (*Posting, error) {
	ref, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if c.Blocks(ref.URL) {
		return nil, fmt.Errorf("posting %s: %w", ref.URL, ErrBlocked)
	}
	var p *Posting
	switch ref.Source {
	case Greenhouse:
		p, err = c.greenhouse(ctx, ref)
	case Lever:
		p, err = c.lever(ctx, ref)
	case Ashby:
		p, err = c.ashby(ctx, ref)
	case Workday:
		p, err = c.workday(ctx, ref)
	case SmartRecruiters:
		p, err = c.smartRecruiters(ctx, ref)
	case Recruitee:
		p, err = c.recruitee(ctx, ref)
	case Personio:
		p, err = c.personio(ctx, ref)
	case Workable:
		p, err = c.workable(ctx, ref)
	case Oracle:
		p, err = c.oracle(ctx, ref)
	default:
		// A company's own careers page on Greenhouse: its API has the
		// posting, with the form's questions.
		if p, err = c.greenhouseHosted(ctx, ref); err == nil {
			ref.Source = Greenhouse
			break
		}
		p, err = c.page(ctx, ref)
	}
	if err == nil && strings.TrimSpace(p.Description) == "" {
		err = ErrNoText
	}
	if err != nil {
		return nil, fmt.Errorf("posting %s: %w", ref.URL, err)
	}
	p.Source, p.Description = ref.Source, cut(p.Description, MaxDescription)
	if p.URL == "" {
		p.URL = ref.URL
	}
	return p, nil
}

func (c *Client) greenhouse(ctx context.Context, ref Ref) (*Posting, error) {
	var j struct {
		AbsoluteURL    string `json:"absolute_url"`
		Title          string `json:"title"`
		CompanyName    string `json:"company_name"`
		FirstPublished string `json:"first_published"`
		Location       struct {
			Name string `json:"name"`
		} `json:"location"`
		Content   string `json:"content"`
		Questions []struct {
			Label    string `json:"label"`
			Required bool   `json:"required"`
			Fields   []struct {
				Type   string `json:"type"`
				Values []struct {
					Label string `json:"label"`
				} `json:"values"`
			} `json:"fields"`
		} `json:"questions"`
	}
	u := "https://" + ref.host + "/v1/boards/" + url.PathEscape(ref.board) + "/jobs/" + ref.id + "?questions=true"
	if err := c.getJSON(ctx, u, &j); err != nil {
		return nil, err
	}
	p := &Posting{
		ApplyURL: j.AbsoluteURL, Title: j.Title, Company: j.CompanyName, Location: j.Location.Name,
		Posted: isoDate(j.FirstPublished), Description: Text(html.UnescapeString(j.Content)),
	}
	for _, q := range j.Questions {
		if len(q.Fields) == 0 {
			continue
		}
		kind, ok := ghKinds[q.Fields[0].Type]
		if !ok {
			continue // hidden fields
		}
		pq := Question{Label: strings.TrimSpace(q.Label), Required: q.Required, Kind: kind}
		for _, v := range q.Fields[0].Values {
			pq.Options = append(pq.Options, v.Label)
		}
		p.Questions = append(p.Questions, pq)
	}
	return p, nil
}

// ghKinds maps Greenhouse field types to Question kinds.
var ghKinds = map[string]string{
	"input_text": "text", "textarea": "textarea", "input_file": "file",
	"multi_value_single_select": "select", "multi_value_multi_select": "multiselect",
}

func (c *Client) lever(ctx context.Context, ref Ref) (*Posting, error) {
	var j struct {
		Text       string `json:"text"`
		HostedURL  string `json:"hostedUrl"`
		ApplyURL   string `json:"applyUrl"`
		CreatedAt  int64  `json:"createdAt"`
		Categories struct {
			Location string `json:"location"`
		} `json:"categories"`
		DescriptionPlain string `json:"descriptionPlain"`
		Lists            []struct {
			Text    string `json:"text"`
			Content string `json:"content"`
		} `json:"lists"`
		AdditionalPlain string `json:"additionalPlain"`
	}
	u := "https://" + ref.host + "/v0/postings/" + url.PathEscape(ref.board) + "/" + ref.id
	if err := c.getJSON(ctx, u, &j); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(j.DescriptionPlain))
	for _, l := range j.Lists {
		fmt.Fprintf(&b, "\n\n%s\n%s", strings.TrimSpace(l.Text), Text(l.Content))
	}
	if s := strings.TrimSpace(j.AdditionalPlain); s != "" {
		b.WriteString("\n\n" + s)
	}
	p := &Posting{URL: j.HostedURL, ApplyURL: j.ApplyURL, Title: j.Text, Company: ref.board,
		Location: j.Categories.Location, Description: b.String()}
	if j.CreatedAt > 0 {
		p.Posted = time.UnixMilli(j.CreatedAt).UTC().Format(time.DateOnly)
	}
	return p, nil
}

func (c *Client) ashby(ctx context.Context, ref Ref) (*Posting, error) {
	type job struct {
		ID               string `json:"id"`
		Title            string `json:"title"`
		Location         string `json:"location"`
		PublishedAt      string `json:"publishedAt"`
		JobURL           string `json:"jobUrl"`
		ApplyURL         string `json:"applyUrl"`
		DescriptionPlain string `json:"descriptionPlain"`
	}
	resp, err := c.get(ctx, "https://"+ref.host+"/posting-api/job-board/"+url.PathEscape(ref.board), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// {"jobs": [...], ...}: the jobs are decoded one at a time, up to ours.
	dec := json.NewDecoder(io.LimitReader(resp.Body, MaxBoard))
	if err := seekArray(dec, "jobs"); err != nil {
		return nil, fmt.Errorf("ashby board: %w", err)
	}
	for dec.More() {
		var j job
		if err := dec.Decode(&j); err != nil {
			return nil, fmt.Errorf("ashby board: %w", err)
		}
		if j.ID == ref.id {
			return &Posting{URL: j.JobURL, ApplyURL: j.ApplyURL, Title: j.Title, Company: ref.board,
				Location: j.Location, Posted: isoDate(j.PublishedAt), Description: j.DescriptionPlain}, nil
		}
	}
	return nil, ErrGone
}

// seekArray reads dec up to the start of the array under key in the
// top-level object, skipping the values before it.
func seekArray(dec *json.Decoder, key string) error {
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return fmt.Errorf("not an object: %v", err)
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if t == key {
			if t, err := dec.Token(); err != nil || t != json.Delim('[') {
				return fmt.Errorf("%s is not an array: %v", key, err)
			}
			return nil
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
	}
	return fmt.Errorf("no %s", key)
}

func (c *Client) workday(ctx context.Context, ref Ref) (*Posting, error) {
	var j struct {
		JobPostingInfo struct {
			Title          string `json:"title"`
			JobDescription string `json:"jobDescription"`
			Location       string `json:"location"`
			StartDate      string `json:"startDate"`
			ExternalURL    string `json:"externalUrl"`
		} `json:"jobPostingInfo"`
		HiringOrganization struct {
			Name string `json:"name"`
		} `json:"hiringOrganization"`
	}
	u := "https://" + ref.host + "/wday/cxs/" + ref.board + ref.id
	if err := c.getJSON(ctx, u, &j); err != nil {
		return nil, err
	}
	i := j.JobPostingInfo
	tenant, _, _ := strings.Cut(ref.board, "/")
	return &Posting{URL: i.ExternalURL, ApplyURL: i.ExternalURL, Title: i.Title, Company: tenant,
		Location: i.Location, Posted: isoDate(i.StartDate), Description: Text(i.JobDescription)}, nil
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

// greenhouseHosted reads a posting on a company's own careers page that
// embeds Greenhouse, from its gh_jid parameter, through Greenhouse's API.
// The board is guessed from the site's name; a wrong guess is a 404, since
// a job id belongs to one board.
func (c *Client) greenhouseHosted(ctx context.Context, ref Ref) (*Posting, error) {
	u, err := url.Parse(ref.URL)
	if err != nil {
		return nil, err
	}
	id := u.Query().Get("gh_jid")
	if !digits.MatchString(id) {
		return nil, ErrGone
	}
	labels := strings.Split(strings.ToLower(u.Hostname()), ".")
	for len(labels) > 2 && (labels[0] == "www" || labels[0] == "careers" || labels[0] == "jobs") {
		labels = labels[1:]
	}
	// acme-ai.com's board may be acme-ai or acmeai.
	boards := []string{labels[0]}
	if b := strings.ReplaceAll(labels[0], "-", ""); b != labels[0] {
		boards = append(boards, b)
	}
	for _, b := range boards {
		if p, err := c.greenhouse(ctx, Ref{host: "boards-api.greenhouse.io", board: b, id: id}); err == nil {
			return p, nil
		}
	}
	return nil, ErrGone
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
