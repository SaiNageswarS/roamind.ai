package jobs

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

// workable is Workable's job pages, apply.workable.com; common in Europe.
type workable struct{}

var wkPath = regexp.MustCompile(`^/([^/]+)/j/([0-9A-Za-z]+)`)

func (workable) Match(u *url.URL) (Ref, bool) {
	if hostOf(u) != "apply.workable.com" {
		return Ref{}, false
	}
	m := wkPath.FindStringSubmatch(u.Path)
	if m == nil {
		return Ref{}, false
	}
	return Ref{Source: Workable, URL: "https://apply.workable.com/" + m[1] + "/j/" + m[2], host: "apply.workable.com", board: m[1], id: m[2]}, true
}

func (workable) Posting(ctx context.Context, c *Client, ref Ref) (*Posting, error) {
	var j struct {
		Title     string `json:"title"`
		Published string `json:"published"`
		Location  struct {
			City    string `json:"city"`
			Country string `json:"country"`
		} `json:"location"`
		Remote       bool   `json:"remote"`
		Description  string `json:"description"`
		Requirements string `json:"requirements"`
		Benefits     string `json:"benefits"`
	}
	u := "https://" + ref.host + "/api/v2/accounts/" + url.PathEscape(ref.board) + "/jobs/" + url.PathEscape(ref.id)
	if err := c.getJSON(ctx, u, &j); err != nil {
		return nil, err
	}
	var where []string
	for _, s := range []string{j.Location.City, j.Location.Country} {
		if s != "" {
			where = append(where, s)
		}
	}
	if j.Remote {
		where = append(where, "remote")
	}
	return &Posting{ApplyURL: ref.URL + "/apply/", Title: j.Title, Company: ref.board,
		Location: strings.Join(where, ", "), Posted: isoDate(j.Published),
		Description: strings.TrimSpace(Text(j.Description) + "\n\n" + Text(j.Requirements) + "\n\n" + Text(j.Benefits))}, nil
}
