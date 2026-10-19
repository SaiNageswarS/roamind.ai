Find open job postings for {{.UserName}} on the web. Today is {{.Date}}.

Roles they want:
{{range .Roles}}
- {{.}}
{{- end}}

Regions they want to work in:
{{range .Regions}}
- {{.}}
{{- end}}
{{- if .Notes}}

Their notes on what they look for:

<notes>
{{.Notes}}
</notes>
{{- end}}

Search applicant tracking sites, where postings link straight to their
application form: job-boards.greenhouse.io, jobs.lever.co,
jobs.ashbyhq.com and myworkdayjobs.com; in Europe and Hong Kong also
jobs.smartrecruiters.com, recruitee.com, jobs.personio.de,
apply.workable.com, teamtailor.com and Oracle careers sites
(oraclecloud.com/hcmUI/CandidateExperience); and also companies' own
career pages. Searches such as `site:job-boards.greenhouse.io "<role>" <region>`
work well; try each role in each region. Open a result with WebFetch when
you need to check that it is still open, in the region, and a match for
the role.

Return up to {{.Max}} postings that are open now, match one of the roles,
and are in one of the regions or remote for it, best matches first. For
each, give the URL of the posting's own page (not a search or listing
page), its title, company and location, and its application form's URL when
you saw it.
