package jobs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	for raw, want := range map[string][2]string{
		"https://job-boards.greenhouse.io/anthropic/jobs/4461450008?gh_src=x":                             {Greenhouse, "https://job-boards.greenhouse.io/anthropic/jobs/4461450008"},
		"https://boards.greenhouse.io/embed/job_app?for=acme&token=123":                                   {Greenhouse, "https://job-boards.greenhouse.io/acme/jobs/123"},
		"https://jobs.lever.co/palantir/6ed76ce8-4156-4b60-b120-403538bd66cd/apply?lever-x=1":             {Lever, "https://jobs.lever.co/palantir/6ed76ce8-4156-4b60-b120-403538bd66cd"},
		"https://jobs.ashbyhq.com/openai/240d459b-696d-43eb-8497-fab3e56ecd9b/application":                {Ashby, "https://jobs.ashbyhq.com/openai/240d459b-696d-43eb-8497-fab3e56ecd9b"},
		"https://nvidia.wd5.myworkdayjobs.com/en-US/NVIDIAExternalCareerSite/job/US-CA/X_JR1":             {Workday, "https://nvidia.wd5.myworkdayjobs.com/NVIDIAExternalCareerSite/job/US-CA/X_JR1"},
		"https://careers.example.com/jobs/42?gh_jid=42#apply":                                             {Other, "https://careers.example.com/jobs/42?gh_jid=42"},
		"https://jobs.smartrecruiters.com/Ubisoft2/744000156377199-r-d-engineer-c-?trid=x":                {SmartRecruiters, "https://jobs.smartrecruiters.com/Ubisoft2/744000156377199"},
		"https://bunq.recruitee.com/o/head-of-product-6/c/new":                                            {Recruitee, "https://bunq.recruitee.com/o/head-of-product-6"},
		"https://personio.jobs.personio.de/job/1834171?language=en&display=en":                            {Personio, "https://personio.jobs.personio.de/job/1834171"},
		"https://apply.workable.com/huggingface/j/DB4D7C0EC8/apply/":                                      {Workable, "https://apply.workable.com/huggingface/j/DB4D7C0EC8"},
		"https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/de/sites/CX_1001/job/210616021/?utm=x": {Oracle, "https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1001/job/210616021"},
	} {
		ref, err := Parse(raw)
		if err != nil || ref.Source != want[0] || ref.URL != want[1] {
			t.Errorf("%s: %+v %v, want %v", raw, ref, err, want)
		}
	}
	for _, raw := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "careers.example.com/jobs"} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("%q parsed", raw)
		}
	}
}

// server answers every request with the fixture its API path names; the
// client's transport sends every host to it.
func server(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture := map[string]string{
			"boards-api.greenhouse.io/v1/boards/anthropic/jobs/4461450008":                          "greenhouse.json",
			"boards-api.greenhouse.io/v1/boards/acmeai/jobs/4461450008":                             "greenhouse.json",
			"api.smartrecruiters.com/v1/companies/Ubisoft2/postings/744000156377199":                "smartrecruiters.json",
			"bunq.recruitee.com/api/offers/head-of-product-6":                                       "recruitee.json",
			"personio.jobs.personio.de/xml":                                                         "personio.xml",
			"apply.workable.com/api/v2/accounts/huggingface/jobs/DB4D7C0EC8":                        "workable.json",
			"jpmc.fa.oraclecloud.com/hcmRestApi/resources/latest/recruitingCEJobRequisitionDetails": "oracle.json",
			"careers.teamtailor.com/jobs/8118064-uk-account-executive-smb":                          "teamtailor.html",
			"api.lever.co/v0/postings/palantir/6ed76ce8-4156-4b60-b120-403538bd66cd":                "lever.json",
			"api.ashbyhq.com/posting-api/job-board/openai":                                          "ashby.json",
			"nvidia.wd5.myworkdayjobs.com/wday/cxs/nvidia/NVIDIAExternalCareerSite/job/US-CA-Santa-Clara/Principal-Block-File-Storage-Software-Engineer--Linux---DGX-Cloud_JR2024421": "workday.json",
		}[r.Host+r.URL.Path]
		switch {
		case r.Host == "app.example.com":
			// Filled in by its script; no text without running it.
			w.Write([]byte(`<html><head><title>Jobs</title></head><body><div id="root"></div><script src="/main.js"></script></body></html>`))
		case r.Host == "guarded.example.com":
			http.Error(w, "checking your browser", http.StatusTooManyRequests)
		case r.Host == "big.example.com":
			// An Ashby board larger than MaxBody, our job last.
			w.Write([]byte(`{"apiVersion":"1","jobs":[`))
			filler := `{"id":"x","title":"Filler","descriptionPlain":"` + strings.Repeat("a", 1<<20) + `"},`
			for range 10 {
				w.Write([]byte(filler))
			}
			w.Write([]byte(`{"id":"240d459b-696d-43eb-8497-fab3e56ecd9b","title":"Last","descriptionPlain":"Found."}]}`))
		case r.Host == "careers.example.com":
			w.Write([]byte(`<html><head><title>ML Engineer &amp; more</title><script>var x = "<p>no</p>";</script></head>
				<body><h1>ML Engineer</h1><p>Build <b>models</b>.</p><ul><li>Go</li><li>JAX</li></ul></body></html>`))
		case fixture == "":
			http.NotFound(w, r)
		default:
			b, err := os.ReadFile("testdata/" + fixture)
			if err != nil {
				t.Error(err)
			}
			w.Write(b)
		}
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	return &Client{HTTP: &http.Client{Timeout: 5 * time.Second, Transport: rewrite{target}}}
}

// rewrite sends requests to target, keeping their Host.
type rewrite struct{ target *url.URL }

func (rw rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = r.URL.Host
	r.URL.Scheme, r.URL.Host = rw.target.Scheme, rw.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func TestFetch(t *testing.T) {
	ctx := context.Background()
	c := server(t)

	p, err := c.Fetch(ctx, "https://job-boards.greenhouse.io/anthropic/jobs/4461450008")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != Greenhouse || p.Title != "Account Executive, AI Native" || p.Company != "Anthropic" || p.Posted != "2024-12-20" ||
		!strings.HasPrefix(p.Description, "About Anthropic") || strings.ContainsAny(p.Description, "<&") {
		t.Errorf("greenhouse = %+v", p)
	}
	if len(p.Questions) != 10 || !reflect.DeepEqual(p.Questions[0], Question{Label: "First Name", Required: true, Kind: "text"}) ||
		p.Questions[4].Kind != "file" || p.Questions[6].Kind != "select" || strings.Join(p.Questions[6].Options, ",") != "Yes,No" {
		t.Errorf("greenhouse questions = %+v", p.Questions)
	}

	p, err = c.Fetch(ctx, "https://jobs.lever.co/palantir/6ed76ce8-4156-4b60-b120-403538bd66cd/apply")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != Lever || p.Title != "Administrative Business Partner" || p.Location != "Singapore, Singapore" ||
		p.Posted == "" || !strings.HasSuffix(p.ApplyURL, "/apply") || !strings.Contains(p.Description, "What We Value") {
		t.Errorf("lever = %+v", p)
	}

	p, err = c.Fetch(ctx, "https://jobs.ashbyhq.com/openai/240d459b-696d-43eb-8497-fab3e56ecd9b")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != Ashby || p.Title != "Research Engineer" || p.Location != "San Francisco" || p.Posted != "2025-04-05" || p.Description == "" {
		t.Errorf("ashby = %+v", p)
	}
	if _, err := c.Fetch(ctx, "https://jobs.ashbyhq.com/openai/00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrGone) {
		t.Errorf("ashby missing: %v", err)
	}

	p, err = c.Fetch(ctx, "https://nvidia.wd5.myworkdayjobs.com/en-US/NVIDIAExternalCareerSite/job/US-CA-Santa-Clara/Principal-Block-File-Storage-Software-Engineer--Linux---DGX-Cloud_JR2024421")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != Workday || !strings.HasPrefix(p.Title, "Principal Block") || p.Company != "nvidia" || p.Posted != "2026-10-16" || strings.Contains(p.Description, "<") {
		t.Errorf("workday = %+v", p)
	}

	p, err = c.Fetch(ctx, "https://careers.example.com/jobs/42")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != Other || p.Title != "ML Engineer & more" || p.Description != "ML Engineer\nBuild models.\n\n- Go\n- JAX" {
		t.Errorf("page = %+v %q", p, p.Description)
	}

	if _, err := c.Fetch(ctx, "https://job-boards.greenhouse.io/anthropic/jobs/1"); !errors.Is(err, ErrGone) {
		t.Errorf("greenhouse missing: %v", err)
	}
}

func TestBlocked(t *testing.T) {
	c := server(t)
	c.Blocked = []string{"Jobs.Example.org", " "}
	for raw, want := range map[string]bool{
		"https://jobs.example.org/view/1": true, "https://de.jobs.example.org/view/1": true,
		"https://example.org/jobs": false, "https://notjobs.example.org/x": false, "https://careers.example.com/jobs/42": false,
	} {
		if got := c.Blocks(raw); got != want {
			t.Errorf("%s: blocked %v, want %v", raw, got, want)
		}
	}
	if _, err := c.Fetch(context.Background(), "https://jobs.example.org/view/1"); !errors.Is(err, ErrBlocked) {
		t.Errorf("blocked fetch: %v", err)
	}
}

// fakeRender renders every page as html, or fails.
type fakeRender struct {
	html string
	urls []string
}

func (f *fakeRender) Render(_ context.Context, u string) (string, error) {
	f.urls = append(f.urls, u)
	if f.html == "" {
		return "", errors.New("no browser")
	}
	return f.html, nil
}

func TestRender(t *testing.T) {
	ctx := context.Background()
	c := server(t)
	rendered := `<html><head><title>Data Scientist</title></head><body><h1>Data Scientist</h1><p>` + strings.Repeat("Work on models. ", 30) + `</p></body></html>`

	// Without a renderer, a page that fills itself in has no posting.
	if _, err := c.Fetch(ctx, "https://app.example.com/job/1"); !errors.Is(err, ErrNoText) {
		t.Errorf("unrendered: %v", err)
	}
	r := &fakeRender{html: rendered}
	c.Render = r
	p, err := c.Fetch(ctx, "https://app.example.com/job/1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != Other || p.Title != "Data Scientist" || !strings.HasPrefix(p.Description, "Data Scientist\nWork on models.") {
		t.Errorf("rendered = %+v", p)
	}
	// A site that refuses a plain request is rendered too.
	if p, err := c.Fetch(ctx, "https://guarded.example.com/job/2"); err != nil || p.Title != "Data Scientist" {
		t.Errorf("guarded = %+v, %v", p, err)
	}
	// Every page off an API is rendered, even one with text in its HTML;
	// a posting its ATS says is gone, or a page that is, isn't.
	r.urls = nil
	if p, _ := c.Fetch(ctx, "https://careers.example.com/jobs/42"); p == nil || p.Title != "Data Scientist" {
		t.Errorf("careers page = %+v", p)
	}
	c.Fetch(ctx, "https://job-boards.greenhouse.io/anthropic/jobs/1")
	c.Fetch(ctx, "https://missing.example.com/job/3")
	if !reflect.DeepEqual(r.urls, []string{"https://careers.example.com/jobs/42"}) {
		t.Errorf("rendered %v", r.urls)
	}
	// A failed render keeps the plain page's text, if any.
	r.html = ""
	if p, err := c.Fetch(ctx, "https://careers.example.com/jobs/42"); err != nil || p.Description == "" {
		t.Errorf("failed render = %+v, %v", p, err)
	}
	if _, err := c.Fetch(ctx, "https://app.example.com/job/1"); !errors.Is(err, ErrNoText) {
		t.Errorf("failed render of an empty page: %v", err)
	}
}

// A company's careers page that embeds Greenhouse is read through its API.
func TestGreenhouseHosted(t *testing.T) {
	p, err := server(t).Fetch(context.Background(), "https://careers.acme-ai.com/open-roles/account-executive?gh_jid=4461450008")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != Greenhouse || p.Title != "Account Executive, AI Native" || len(p.Questions) != 10 ||
		p.URL != "https://careers.acme-ai.com/open-roles/account-executive?gh_jid=4461450008" {
		t.Errorf("hosted = %+v", p)
	}
}

// An Ashby board larger than MaxBody is read as a stream.
func TestAshbyBigBoard(t *testing.T) {
	p, err := server(t).ashby(context.Background(), Ref{host: "big.example.com", board: "x", id: "240d459b-696d-43eb-8497-fab3e56ecd9b"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Last" || p.Description != "Found." {
		t.Errorf("big board = %+v", p)
	}
}

// The applicant tracking systems common in Europe and Hong Kong.
func TestFetchEuropeHongKong(t *testing.T) {
	ctx := context.Background()
	c := server(t)
	for _, tc := range []struct {
		url, source, title, company, location, posted, text string
		questions                                           int
	}{
		{"https://jobs.smartrecruiters.com/Ubisoft2/744000156377199-r-d-engineer-c-", SmartRecruiters, "R&D Engineer (C++)", "Ubisoft", "Pune, MH, India", "2026-10-19", "", 0},
		{"https://bunq.recruitee.com/o/head-of-product-6", Recruitee, "Head of Product", "bunq", "Amsterdam, Noord-Holland, Netherlands", "2026-10-15", "", 15},
		{"https://personio.jobs.personio.de/job/1834171", Personio, "Staff Software Engineer, Data Platform", "Personio SE & Co. KG", "Munich", "2024-11-13", "The Role: How you'll make an impact at Personio", 0},
		{"https://apply.workable.com/huggingface/j/DB4D7C0EC8/", Workable, "Senior Open-Source Python Engineer, ML Developer Tools - EMEA Remote", "huggingface", "France, remote", "", "", 0},
		{"https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1001/job/210616021", Oracle, "Investment Banking - EMEA Real Estate - Analyst / Associate - London", "jpmc", "LONDON, LONDON, United Kingdom", "2026-10-16", "", 0},
	} {
		p, err := c.Fetch(ctx, tc.url)
		if err != nil {
			t.Errorf("%s: %v", tc.source, err)
			continue
		}
		if p.Source != tc.source || p.Title != tc.title || p.Company != tc.company || p.Location != tc.location ||
			(tc.posted != "" && p.Posted != tc.posted) || len(p.Description) < 500 || strings.Contains(p.Description, "<p>") ||
			!strings.Contains(p.Description, tc.text) || len(p.Questions) != tc.questions {
			t.Errorf("%s = %+v (%d questions)", tc.source, p, len(p.Questions))
		}
	}
	// Recruitee's form: choices keep their options, info boxes are left out.
	p, _ := c.Fetch(ctx, "https://bunq.recruitee.com/o/head-of-product-6")
	if q := p.Questions[1]; q.Kind != "select" || !q.Required || len(q.Options) != 6 || strings.Contains(q.Label, "<") {
		t.Errorf("recruitee question = %+v", q)
	}
	if _, err := c.Fetch(ctx, "https://personio.jobs.personio.de/job/1"); !errors.Is(err, ErrGone) {
		t.Errorf("personio missing: %v", err)
	}
}

// A page that carries its JobPosting data is read from it, unrendered.
func TestJobPostingData(t *testing.T) {
	c := server(t)
	r := &fakeRender{html: "<p>rendered</p>"}
	c.Render = r
	p, err := c.Fetch(context.Background(), "https://careers.teamtailor.com/jobs/8118064-uk-account-executive-smb")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != Other || p.Title != "UK Account Executive - SMB" || p.Company != "Teamtailor" || p.Posted != "2026-07-24" ||
		!strings.Contains(p.Location, "London") || len(p.Description) < 1000 || strings.Contains(p.Description, "<") || len(r.urls) != 0 {
		t.Errorf("teamtailor = %+v, rendered %v", p, r.urls)
	}
}
