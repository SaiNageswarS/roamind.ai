package jobs

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

// recruitee is Recruitee's careers sites, {company}.recruitee.com; common
// in the Netherlands and Europe. Its API has the application form's
// questions.
type recruitee struct{}

var rtPath = regexp.MustCompile(`^/o/([^/]+)`)

func (recruitee) Match(u *url.URL) (Ref, bool) {
	host := hostOf(u)
	if !strings.HasSuffix(host, ".recruitee.com") {
		return Ref{}, false
	}
	m := rtPath.FindStringSubmatch(u.Path)
	if m == nil {
		return Ref{}, false
	}
	company, _, _ := strings.Cut(host, ".")
	return Ref{Source: Recruitee, URL: "https://" + host + "/o/" + m[1], host: host, board: company, id: m[1]}, true
}

func (recruitee) Posting(ctx context.Context, c *Client, ref Ref) (*Posting, error) {
	var j struct {
		Offer struct {
			Title           string `json:"title"`
			CompanyName     string `json:"company_name"`
			Location        string `json:"location"`
			PublishedAt     string `json:"published_at"`
			CareersURL      string `json:"careers_url"`
			CareersApplyURL string `json:"careers_apply_url"`
			Description     string `json:"description"`
			Requirements    string `json:"requirements"`
			OpenQuestions   []struct {
				Body     string `json:"body"`
				Kind     string `json:"kind"`
				Required bool   `json:"required"`
				Options  []struct {
					Body string `json:"body"`
				} `json:"open_question_options"`
			} `json:"open_questions"`
		} `json:"offer"`
	}
	u := "https://" + ref.host + "/api/offers/" + url.PathEscape(ref.id)
	if err := c.getJSON(ctx, u, &j); err != nil {
		return nil, err
	}
	o := j.Offer
	p := &Posting{URL: o.CareersURL, ApplyURL: o.CareersApplyURL, Title: o.Title, Company: o.CompanyName,
		Location: o.Location, Posted: isoDate(o.PublishedAt),
		Description: strings.TrimSpace(Text(o.Description) + "\n\n" + Text(o.Requirements))}
	for _, q := range o.OpenQuestions {
		kind, ok := rtKinds[q.Kind]
		if !ok {
			continue // info boxes
		}
		pq := Question{Label: Text(q.Body), Required: q.Required, Kind: kind}
		for _, v := range q.Options {
			pq.Options = append(pq.Options, v.Body)
		}
		if q.Kind == "boolean" {
			pq.Options = []string{"Yes", "No"}
		}
		p.Questions = append(p.Questions, pq)
	}
	return p, nil
}

// rtKinds maps Recruitee question kinds to Question kinds.
var rtKinds = map[string]string{
	"string": "text", "text": "textarea", "date": "text", "file": "file", "boolean": "select",
	"single_choice": "select", "multi_choice": "multiselect",
}
