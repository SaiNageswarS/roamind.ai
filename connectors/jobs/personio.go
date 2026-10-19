package jobs

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"

	"roamind.ai/connectors/safehttp"
)

// personio is Personio's job pages, {company}.jobs.personio.de or .com;
// common in Germany, Austria and Switzerland. Its API is the company's XML
// feed of every open position.
type personio struct{}

var (
	peHost = regexp.MustCompile(`^[a-z0-9-]+\.jobs\.personio\.(de|com)$`)
	pePath = regexp.MustCompile(`^/job/(\d+)`)
)

func (personio) Match(u *url.URL) (Ref, bool) {
	host := hostOf(u)
	if !peHost.MatchString(host) {
		return Ref{}, false
	}
	m := pePath.FindStringSubmatch(u.Path)
	if m == nil {
		return Ref{}, false
	}
	company, _, _ := strings.Cut(host, ".")
	return Ref{Source: Personio, URL: "https://" + host + "/job/" + m[1], host: host, board: company, id: m[1]}, true
}

func (personio) Posting(ctx context.Context, c *Client, ref Ref) (*Posting, error) {
	resp, err := c.get(ctx, "https://"+ref.host+"/xml?language=en", "application/xml")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := safehttp.ReadAll(resp.Body, MaxBody)
	if err != nil {
		return nil, err
	}
	var feed struct {
		Positions []struct {
			ID         string `xml:"id"`
			Subcompany string `xml:"subcompany"`
			Office     string `xml:"office"`
			Name       string `xml:"name"`
			CreatedAt  string `xml:"createdAt"`
			Sections   []struct {
				Name  string `xml:"name"`
				Value string `xml:"value"`
			} `xml:"jobDescriptions>jobDescription"`
		} `xml:"position"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("personio feed: %w", err)
	}
	for _, pos := range feed.Positions {
		if pos.ID != ref.id {
			continue
		}
		var b strings.Builder
		for _, s := range pos.Sections {
			if text := Text(s.Value); text != "" {
				fmt.Fprintf(&b, "%s\n%s\n\n", strings.TrimSpace(html.UnescapeString(s.Name)), text)
			}
		}
		company := strings.TrimSpace(html.UnescapeString(pos.Subcompany))
		if company == "" {
			company = ref.board
		}
		return &Posting{ApplyURL: ref.URL, Title: pos.Name, Company: company,
			Location: pos.Office, Posted: isoDate(pos.CreatedAt), Description: strings.TrimSpace(b.String())}, nil
	}
	return nil, ErrGone
}
