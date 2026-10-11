Draft today's exercise ({{.Date}}) for {{.UserName}}'s learning goal
"{{.GoalTitle}}".

The goal note, with what {{.UserName}} wants to reach and their exercise
preferences:

<goal>
{{.Goal}}
</goal>
{{if .Exercises}}
{{.UserName}}'s latest exercises for this goal, newest first, each with
their answer and how it went:
{{range .Exercises}}
<exercise>
{{.}}
</exercise>
{{end}}
Calibrate from them: choose the topic and difficulty from the latest results
and their next lines. Step up after correct answers, stay at the level after
partial ones, and revisit a topic more simply after incorrect ones.
{{- else}}
This is {{.UserName}}'s first exercise for this goal: start at intro or easy,
matching the level the goal note describes.
{{- end}}

Follow the exercise preferences in the goal note. Set one exercise that
takes about ten minutes; it can have several numbered items or a longer
passage, in whatever format best practises the topic. End it with how to
reply, such as one numbered line per item. Write the exercise in light
markdown, exactly as {{.UserName}} will see it in Slack, and return it with
its topic, difficulty and format.
