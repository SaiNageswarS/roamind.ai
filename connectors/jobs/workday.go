package jobs

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

// workday is Workday's careers sites, on myworkdayjobs.com.
type workday struct{}

var wdPath = regexp.MustCompile(`^/(?:[a-z]{2}-[A-Z]{2}/)?([^/]+)(/job/.+)$`)

func (workday) Match(u *url.URL) (Ref, bool) {
	host := hostOf(u)
	if !strings.HasSuffix(host, ".myworkdayjobs.com") {
		return Ref{}, false
	}
	m := wdPath.FindStringSubmatch(u.Path)
	if m == nil {
		return Ref{}, false
	}
	tenant, _, _ := strings.Cut(host, ".")
	return Ref{Source: Workday, URL: "https://" + host + "/" + m[1] + m[2], host: host, board: tenant + "/" + m[1], id: m[2]}, true
}

func (workday) Posting(ctx context.Context, c *Client, ref Ref) (*Posting, error) {
	var j struct {
		JobPostingInfo struct {
			Title          string `json:"title"`
			JobDescription string `json:"jobDescription"`
			Location       string `json:"location"`
			StartDate      string `json:"startDate"`
			ExternalURL    string `json:"externalUrl"`
		} `json:"jobPostingInfo"`
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
