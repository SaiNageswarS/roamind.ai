package jobs

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"net/url"
	"strings"

	"roamind.ai/connectors/safehttp"
)

// The applicant tracking systems common in Europe and Hong Kong, beyond
// Greenhouse, Lever, Ashby and Workday; each has a public posting API.

// srSection is a titled section of a SmartRecruiters job ad, in HTML.
type srSection struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

func (c *Client) smartRecruiters(ctx context.Context, ref Ref) (*Posting, error) {
	var j struct {
		Name    string `json:"name"`
		Company struct {
			Name string `json:"name"`
		} `json:"company"`
		Location struct {
			FullLocation string `json:"fullLocation"`
		} `json:"location"`
		ReleasedDate string `json:"releasedDate"`
		PostingURL   string `json:"postingUrl"`
		ApplyURL     string `json:"applyUrl"`
		JobAd        struct {
			Sections struct {
				CompanyDescription    srSection `json:"companyDescription"`
				JobDescription        srSection `json:"jobDescription"`
				Qualifications        srSection `json:"qualifications"`
				AdditionalInformation srSection `json:"additionalInformation"`
			} `json:"sections"`
		} `json:"jobAd"`
	}
	u := "https://" + ref.host + "/v1/companies/" + url.PathEscape(ref.board) + "/postings/" + ref.id
	if err := c.getJSON(ctx, u, &j); err != nil {
		return nil, err
	}
	s := j.JobAd.Sections
	return &Posting{URL: j.PostingURL, ApplyURL: j.ApplyURL, Title: j.Name, Company: j.Company.Name,
		Location: j.Location.FullLocation, Posted: isoDate(j.ReleasedDate),
		Description: sections(s.CompanyDescription, s.JobDescription, s.Qualifications, s.AdditionalInformation)}, nil
}

// sections joins a job ad's sections as text.
func sections(ss ...srSection) string {
	var b strings.Builder
	for _, v := range ss {
		text := Text(v.Text)
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if v.Title != "" {
			b.WriteString(strings.TrimSpace(v.Title) + "\n")
		}
		b.WriteString(text)
	}
	return b.String()
}

func (c *Client) recruitee(ctx context.Context, ref Ref) (*Posting, error) {
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
		kind, ok := recruiteeKinds[q.Kind]
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

// recruiteeKinds maps Recruitee question kinds to Question kinds.
var recruiteeKinds = map[string]string{
	"string": "text", "text": "textarea", "date": "text", "file": "file", "boolean": "select",
	"single_choice": "select", "multi_choice": "multiselect",
}

// personio reads the posting from its company's XML feed, which lists
// every open position.
func (c *Client) personio(ctx context.Context, ref Ref) (*Posting, error) {
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
		return &Posting{ApplyURL: ref.URL, Title: pos.Name, Company: orBoard(pos.Subcompany, ref.board),
			Location: pos.Office, Posted: isoDate(pos.CreatedAt), Description: strings.TrimSpace(b.String())}, nil
	}
	return nil, ErrGone
}

func (c *Client) workable(ctx context.Context, ref Ref) (*Posting, error) {
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

// oracle reads a posting from Oracle Recruiting Cloud's candidate
// experience API, which the careers site itself calls.
func (c *Client) oracle(ctx context.Context, ref Ref) (*Posting, error) {
	var j struct {
		Items []struct {
			Title                       string `json:"Title"`
			PrimaryLocation             string `json:"PrimaryLocation"`
			ExternalPostedStartDate     string `json:"ExternalPostedStartDate"`
			ExternalDescriptionStr      string `json:"ExternalDescriptionStr"`
			ExternalResponsibilitiesStr string `json:"ExternalResponsibilitiesStr"`
			ExternalQualificationsStr   string `json:"ExternalQualificationsStr"`
			CorporateDescriptionStr     string `json:"CorporateDescriptionStr"`
		} `json:"items"`
	}
	finder := fmt.Sprintf(`ById;Id="%s",siteNumber=%s`, ref.id, ref.board)
	u := "https://" + ref.host + "/hcmRestApi/resources/latest/recruitingCEJobRequisitionDetails?expand=all&onlyData=true&finder=" + url.QueryEscape(finder)
	if err := c.getJSON(ctx, u, &j); err != nil {
		return nil, err
	}
	if len(j.Items) == 0 {
		return nil, ErrGone
	}
	i := j.Items[0]
	tenant, _, _ := strings.Cut(ref.host, ".")
	var parts []string
	for _, s := range []string{i.ExternalDescriptionStr, i.ExternalResponsibilitiesStr, i.ExternalQualificationsStr, i.CorporateDescriptionStr} {
		if t := Text(s); t != "" {
			parts = append(parts, t)
		}
	}
	return &Posting{ApplyURL: ref.URL, Title: i.Title, Company: tenant, Location: i.PrimaryLocation,
		Posted: isoDate(i.ExternalPostedStartDate), Description: strings.Join(parts, "\n\n")}, nil
}

func orBoard(s, board string) string {
	if s = strings.TrimSpace(html.UnescapeString(s)); s != "" {
		return s
	}
	return board
}
