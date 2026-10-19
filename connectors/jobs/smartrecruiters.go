package jobs

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

// smartRecruiters is SmartRecruiters' job pages, jobs.smartrecruiters.com;
// common in Europe.
type smartRecruiters struct{}

var srPath = regexp.MustCompile(`^/([^/]+)/(\d+)`)

func (smartRecruiters) Match(u *url.URL) (Ref, bool) {
	host := hostOf(u)
	if host != "jobs.smartrecruiters.com" && host != "careers.smartrecruiters.com" {
		return Ref{}, false
	}
	m := srPath.FindStringSubmatch(u.Path)
	if m == nil {
		return Ref{}, false
	}
	return Ref{Source: SmartRecruiters, URL: "https://jobs.smartrecruiters.com/" + m[1] + "/" + m[2], host: "api.smartrecruiters.com", board: m[1], id: m[2]}, true
}

// srSection is a titled section of a SmartRecruiters job ad, in HTML.
type srSection struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

func (smartRecruiters) Posting(ctx context.Context, c *Client, ref Ref) (*Posting, error) {
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
		Description: srText(s.CompanyDescription, s.JobDescription, s.Qualifications, s.AdditionalInformation)}, nil
}

// srText joins a job ad's sections as text, each under its title.
func srText(ss ...srSection) string {
	var b strings.Builder
	for _, s := range ss {
		text := Text(s.Text)
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if s.Title != "" {
			b.WriteString(strings.TrimSpace(s.Title) + "\n")
		}
		b.WriteString(text)
	}
	return b.String()
}
