Judge how well each job posting below suits {{.UserName}}.

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

Their resume:

<resume>
{{.Resume}}
</resume>

The postings:
{{range .Candidates}}
<posting id="{{.ID}}">
{{.Title}}{{if .Company}}, {{.Company}}{{end}}{{if .Location}} ({{.Location}}){{end}}{{if .Posted}}, posted {{.Posted}}{{end}}
{{.Description}}
</posting>
{{end}}
Judge each posting from its text above against the roles, regions, notes
and resume. Score 9-10 when it matches a wanted role and region and the
resume meets its main requirements; 6-8 when it is a good match with a gap
or two; 3-5 when it is a stretch or only partly the role; 0-2 when it is
another kind of role, outside the regions, or closed. Give two to four
short reasons, written to {{.UserName}}: what fits and what is missing.
