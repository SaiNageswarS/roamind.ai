package agent

import (
	"bytes"
	_ "embed"
	"text/template"
)

//go:embed prompts/draft_exercise.md
var draftExerciseTmpl string

var draftExercisePrompt = template.Must(template.New("draft_exercise").Parse(draftExerciseTmpl))

// ExerciseDraft is what the agent is given to draft a daily exercise.
type ExerciseDraft struct {
	UserName  string
	Date      string // ISO date
	GoalTitle string
	// Goal is the goal note's content, exercise preferences included.
	Goal string
	// Exercises are the contents of the latest exercise notes, newest first.
	Exercises []string
}

// DraftExercisePrompt is the turn text that has the agent draft an
// exercise; run it with OutputSchema DraftExerciseSchema.
func DraftExercisePrompt(d ExerciseDraft) (string, error) {
	var b bytes.Buffer
	err := draftExercisePrompt.Execute(&b, d)
	return b.String(), err
}

// DraftExerciseSchema is the structured output of a drafted exercise; the
// difficulty values are those of the Exercise schema.
const DraftExerciseSchema = `{
  "type": "object",
  "properties": {
    "topic": {"type": "string", "description": "The skill or concept the exercise practises, in a few words"},
    "difficulty": {"type": "string", "enum": ["intro", "easy", "medium", "hard", "stretch"]},
    "format": {"type": "string", "description": "The kind of exercise and how many items it has"},
    "exercise": {"type": "string", "description": "The full exercise in light markdown, as posted"}
  },
  "required": ["topic", "difficulty", "format", "exercise"],
  "additionalProperties": false
}`

// DraftedExercise is the structured output of DraftExerciseSchema.
type DraftedExercise struct {
	Topic      string `json:"topic"`
	Difficulty string `json:"difficulty"`
	Format     string `json:"format"`
	Exercise   string `json:"exercise"`
}
