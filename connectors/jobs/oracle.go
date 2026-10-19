package jobs

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// oracle is Oracle Recruiting Cloud's careers sites, on the employer's
// oraclecloud.com tenant; common with banks, in Hong Kong and elsewhere.
// Its API is the one the careers site itself calls.
type oracle struct{}

var orPath = regexp.MustCompile(`^/hcmUI/CandidateExperience/[^/]+/sites/([^/]+)/job/(\d+)`)

func (oracle) Match(u *url.URL) (Ref, bool) {
	host := hostOf(u)
	if !strings.HasSuffix(host, ".oraclecloud.com") {
		return Ref{}, false
	}
	m := orPath.FindStringSubmatch(u.Path)
	if m == nil {
		return Ref{}, false
	}
	return Ref{Source: Oracle, URL: "https://" + host + "/hcmUI/CandidateExperience/en/sites/" + m[1] + "/job/" + m[2], host: host, board: m[1], id: m[2]}, true
}

func (oracle) Posting(ctx context.Context, c *Client, ref Ref) (*Posting, error) {
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
	var parts []string
	for _, s := range []string{i.ExternalDescriptionStr, i.ExternalResponsibilitiesStr, i.ExternalQualificationsStr, i.CorporateDescriptionStr} {
		if t := Text(s); t != "" {
			parts = append(parts, t)
		}
	}
	tenant, _, _ := strings.Cut(ref.host, ".")
	return &Posting{ApplyURL: ref.URL, Title: i.Title, Company: tenant, Location: i.PrimaryLocation,
		Posted: isoDate(i.ExternalPostedStartDate), Description: strings.Join(parts, "\n\n")}, nil
}
