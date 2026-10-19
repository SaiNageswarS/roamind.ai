package jobs

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// lever is Lever's job sites, jobs.lever.co and its EU instance.
type lever struct{}

// idPath is a Lever or Ashby posting's path: its site and UUID.
var idPath = regexp.MustCompile(`^/([^/]+)/([0-9a-f-]{36})`)

func (lever) Match(u *url.URL) (Ref, bool) {
	host := hostOf(u)
	if host != "jobs.lever.co" && host != "jobs.eu.lever.co" {
		return Ref{}, false
	}
	m := idPath.FindStringSubmatch(u.Path)
	if m == nil {
		return Ref{}, false
	}
	api := "api.lever.co"
	if host == "jobs.eu.lever.co" {
		api = "api.eu.lever.co"
	}
	return Ref{Source: Lever, URL: "https://" + host + "/" + m[1] + "/" + m[2], host: api, board: m[1], id: m[2]}, true
}

func (lever) Posting(ctx context.Context, c *Client, ref Ref) (*Posting, error) {
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
