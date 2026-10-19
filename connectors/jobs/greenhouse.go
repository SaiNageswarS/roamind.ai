package jobs

import (
	"context"
	"html"
	"net/url"
	"regexp"
	"strings"
)

// greenhouse is Greenhouse's job boards, job-boards.greenhouse.io and
// their embeds; its API has the application form's questions.
type greenhouse struct{}

var (
	ghPath = regexp.MustCompile(`^/([^/]+)/jobs/(\d+)`)
	digits = regexp.MustCompile(`^\d+$`)
)

func (greenhouse) Match(u *url.URL) (Ref, bool) {
	host := hostOf(u)
	if !strings.HasSuffix(host, "greenhouse.io") {
		return Ref{}, false
	}
	if m := ghPath.FindStringSubmatch(u.Path); m != nil {
		api := "boards-api.greenhouse.io"
		if strings.Contains(host, ".eu.") {
			api = "boards-api.eu.greenhouse.io"
		}
		return Ref{Source: Greenhouse, URL: "https://" + host + "/" + m[1] + "/jobs/" + m[2], host: api, board: m[1], id: m[2]}, true
	}
	if q := u.Query(); u.Path == "/embed/job_app" && q.Get("for") != "" && digits.MatchString(q.Get("token")) {
		return Ref{Source: Greenhouse, URL: "https://job-boards.greenhouse.io/" + q.Get("for") + "/jobs/" + q.Get("token"),
			host: "boards-api.greenhouse.io", board: q.Get("for"), id: q.Get("token")}, true
	}
	return Ref{}, false
}

func (greenhouse) Posting(ctx context.Context, c *Client, ref Ref) (*Posting, error) {
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

// greenhouseHosted is a company's own careers page embedding Greenhouse,
// with the posting's id in a gh_jid parameter. The board is guessed from
// the site's name; a wrong guess is a 404, since a job id belongs to one
// board, and then the page is read instead. Until its posting is read, the
// page counts as the company's own.
type greenhouseHosted struct{}

func (greenhouseHosted) Match(u *url.URL) (Ref, bool) {
	id := u.Query().Get("gh_jid")
	if !digits.MatchString(id) {
		return Ref{}, false
	}
	labels := strings.Split(hostOf(u), ".")
	for len(labels) > 2 && (labels[0] == "www" || labels[0] == "careers" || labels[0] == "jobs") {
		labels = labels[1:]
	}
	return Ref{Source: Other, URL: withoutFragment(u), host: "boards-api.greenhouse.io", board: labels[0], id: id}, true
}

func (greenhouseHosted) Posting(ctx context.Context, c *Client, ref Ref) (*Posting, error) {
	// acme-ai.com's board may be acme-ai or acmeai.
	boards := []string{ref.board}
	if b := strings.ReplaceAll(ref.board, "-", ""); b != ref.board {
		boards = append(boards, b)
	}
	for _, b := range boards {
		ref.board = b
		if p, err := (greenhouse{}).Posting(ctx, c, ref); err == nil {
			p.Source = Greenhouse
			return p, nil
		}
	}
	return nil, errNotHere
}
