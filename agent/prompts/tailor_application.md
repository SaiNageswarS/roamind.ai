Write {{.UserName}}'s application for the job posting below. Today is
{{.Date}}.

Their resume, from their profile:

<resume>
{{.Resume}}
</resume>
{{- if .Notes}}

Their notes on what they look for and how they want to come across:

<notes>
{{.Notes}}
</notes>
{{- end}}

The posting:

<posting>
{{.Posting.Title}}{{if .Posting.Company}}, {{.Posting.Company}}{{end}}{{if .Posting.Location}} ({{.Posting.Location}}){{end}}
{{.Posting.Description}}
</posting>

Write two things. {{.UserName}} reviews them and applies on their own, with
their own resume.

1. cover_letter_md: a cover letter of three or four short paragraphs, in
   Markdown, addressed to the company's hiring team: why this role, the two
   or three things from the resume that match it best, with specifics, and
   a short close. Write in {{.UserName}}'s voice, plainly.
2. answers: a drafted answer for each question of the application form.
{{- if .Questions}} The form's questions:
{{range .Questions}}
   - {{.Label}}{{if .Required}} (required){{end}}, {{.Kind}}{{if .Options}}, one of: {{range $i, $o := .Options}}{{if $i}}; {{end}}{{$o}}{{end}}{{end}}
{{- end}}

   Answer every question in that order, with its text as given. Fill
   name, email{{if .UserEmail}} ({{.UserEmail}}){{end}}, phone and profile
   links from the resume. For a select, answer with one of its options. For
   a file, answer "Attach your resume" or "Attach the cover letter". When
   the resume and notes don't hold the answer, such as a start date, salary or work authorization, write
   "[{{.UserName}} to answer]" so they fill it in.
{{- else}} This form's questions are not published, so answer the ones
   most forms ask: why this company, why this role, and the most relevant
   experience, each in a short paragraph, marked not required.
{{- end}}
