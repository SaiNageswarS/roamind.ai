package agent

import (
	"bytes"
	_ "embed"
	"text/template"
)

var (
	//go:embed prompts/find_jobs.md
	findJobsTmpl string
	//go:embed prompts/rank_jobs.md
	rankJobsTmpl string
	//go:embed prompts/tailor_application.md
	tailorApplicationTmpl string

	findJobsPrompt          = template.Must(template.New("find_jobs").Parse(findJobsTmpl))
	rankJobsPrompt          = template.Must(template.New("rank_jobs").Parse(rankJobsTmpl))
	tailorApplicationPrompt = template.Must(template.New("tailor_application").Parse(tailorApplicationTmpl))
)

// JobSearch is what the agent is given to find job postings for a user.
type JobSearch struct {
	UserName string
	Date     string // ISO date
	Roles    []string
	Regions  []string
	// Notes are the user's own notes on what they look for.
	Notes string
	// Max is how many postings to return at most.
	Max int
}

// FindJobsPrompt is the turn text that has the agent search the web for
// postings; run it with ToolsWeb and OutputSchema FoundJobsSchema.
func FindJobsPrompt(s JobSearch) (string, error) {
	return execute(findJobsPrompt, s)
}

// FoundJobsSchema is the structured output of a job search: the postings
// found.
const FoundJobsSchema = `{
  "type": "object",
  "properties": {
    "postings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "url": {"type": "string", "description": "The posting's own page"},
          "title": {"type": "string"},
          "company": {"type": "string"},
          "location": {"type": "string"},
          "apply_url": {"type": "string", "description": "Where the application form is, when the page shows it; else empty"}
        },
        "required": ["url", "title", "company", "location", "apply_url"],
        "additionalProperties": false
      }
    }
  },
  "required": ["postings"],
  "additionalProperties": false
}`

// FoundJobs is the structured output of FoundJobsSchema.
type FoundJobs struct {
	Postings []FoundJob `json:"postings"`
}

// FoundJob is a posting a search found.
type FoundJob struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Company  string `json:"company"`
	Location string `json:"location"`
	ApplyURL string `json:"apply_url"`
}

// JobRanking is what the agent is given to judge how well postings suit
// a user.
type JobRanking struct {
	UserName string
	Roles    []string
	Regions  []string
	Notes    string
	// Resume is the text of the user's resume.
	Resume     string
	Candidates []JobCandidate
}

// JobCandidate is a posting, as read from its site.
type JobCandidate struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Company     string `json:"company"`
	Location    string `json:"location"`
	Posted      string `json:"posted,omitempty"` // ISO date
	Description string `json:"description"`
}

// RankJobsPrompt is the turn text that has the agent judge postings for a
// user; run it with ToolsNone and OutputSchema RankJobsSchema.
func RankJobsPrompt(r JobRanking) (string, error) {
	return execute(rankJobsPrompt, r)
}

// RankJobsSchema is the structured output of a ranking: each candidate's
// fit, with its reasons.
const RankJobsSchema = `{
  "type": "object",
  "properties": {
    "judgements": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string", "description": "The candidate's id"},
          "score": {"type": "integer", "minimum": 0, "maximum": 10, "description": "How well the posting suits the user"},
          "reasons": {"type": "array", "items": {"type": "string"}, "description": "Two to four short reasons for the score, for the user"}
        },
        "required": ["id", "score", "reasons"],
        "additionalProperties": false
      }
    }
  },
  "required": ["judgements"],
  "additionalProperties": false
}`

// RankedJobs is the structured output of RankJobsSchema.
type RankedJobs struct {
	Judgements []JobFit `json:"judgements"`
}

// JobFit is the judgement of one candidate.
type JobFit struct {
	ID      string   `json:"id"`
	Score   int      `json:"score"`
	Reasons []string `json:"reasons"`
}

// Tailoring is what the agent is given to write an application.
type Tailoring struct {
	UserName  string
	UserEmail string
	Date      string // ISO date
	Notes     string
	Resume    string
	Posting   JobCandidate
	// Questions are the application form's, when its site publishes them.
	Questions []FormQuestion
}

// FormQuestion is a question of an application form.
type FormQuestion struct {
	Label    string
	Required bool
	Kind     string // text, textarea, select, multiselect or file
	Options  []string
}

// TailorApplicationPrompt is the turn text that has the agent write a CV,
// cover letter and form answers for a posting; run it with ToolsNone and
// OutputSchema TailorApplicationSchema.
func TailorApplicationPrompt(t Tailoring) (string, error) {
	return execute(tailorApplicationPrompt, t)
}

// TailorApplicationSchema is the structured output of an application.
const TailorApplicationSchema = `{
  "type": "object",
  "properties": {
    "cv_md": {"type": "string", "description": "The tailored CV, in Markdown"},
    "cover_letter_md": {"type": "string", "description": "The cover letter, in Markdown"},
    "answers": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "question": {"type": "string"},
          "required": {"type": "boolean"},
          "answer": {"type": "string"}
        },
        "required": ["question", "required", "answer"],
        "additionalProperties": false
      }
    }
  },
  "required": ["cv_md", "cover_letter_md", "answers"],
  "additionalProperties": false
}`

// TailoredApplication is the structured output of TailorApplicationSchema.
type TailoredApplication struct {
	CV          string       `json:"cv_md"`
	CoverLetter string       `json:"cover_letter_md"`
	Answers     []FormAnswer `json:"answers"`
}

// FormAnswer is a drafted answer to a form question.
type FormAnswer struct {
	Question string `json:"question"`
	Required bool   `json:"required"`
	Answer   string `json:"answer"`
}

func execute(t *template.Template, data any) (string, error) {
	var b bytes.Buffer
	err := t.Execute(&b, data)
	return b.String(), err
}
