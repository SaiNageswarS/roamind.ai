Judge the candidates for {{.UserName}}'s news-shorts digest of {{.Date}} at
{{.Time}}, from the feeds of their interest "{{.Interest}}". The coverage
day is {{.Date}}.

The digest exists to help {{.UserName}} live a better life: the items that
make it should leave them better informed, healthier, calmer or more
hopeful. Judge each candidate only from its title, source, date and content
below, by this rubric:

<rubric>
{{.Rubric}}
</rubric>

The candidates:
{{range .Candidates}}
<candidate id="{{.ID}}">
{{.Title}}
{{.Source}}{{if .Published}}, {{.Published}}{{end}}
{{.Content}}
</candidate>
{{end}}
{{- if .RecentStories}}
Stories sent to {{.UserName}} lately, each with its slug and the title
sent for it:
{{range .RecentStories}}
- {{.Story}}: {{.Title}}
{{- end}}

When a candidate reports one of these stories, give it that story's slug.
{{end}}
Return a judgement for every candidate: its id; a score from 0 to 10 for
each criterion the rubric defines, under the rubric's names; include, true
when it belongs in the digest at all as the rubric decides; its category
when the rubric lists categories, else empty; its story, a 2-5 word
lowercase-hyphenated slug naming the underlying event, the same for every
report of that event; and a summary of one or two plain sentences on what
it is and why it is worth {{.UserName}}'s time.
