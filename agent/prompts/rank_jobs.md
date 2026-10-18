Judge how well each job posting below suits {{.UserName}}.
{{- if .Roles}}

Roles they want:
{{range .Roles}}
- {{.}}
{{- end}}
{{- end}}
{{- if .Regions}}

Regions they want to work in:
{{range .Regions}}
- {{.}}
{{- end}}
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
{{- if or .Roles .Regions}}
Judge each posting from its text above against the roles, regions, notes
and resume. Score 9-10 when it matches a wanted role and region and the
resume meets its main requirements; 6-8 when it is a good match with a gap
or two; 3-5 when it is a stretch or only partly the role; 0-2 when it is
another kind of role, outside the regions, or closed.
{{- else}}
{{.UserName}} picked these postings themselves. Judge each from its text
above against the resume{{if .Notes}} and notes{{end}}. Score 9-10 when the
resume meets its main requirements; 6-8 when it is a good match with a gap
or two; 3-5 when it is a stretch; 0-2 when it is far from their experience,
or closed.
{{- end}} Give two to four short reasons, written to {{.UserName}}: what
fits and what is missing.
