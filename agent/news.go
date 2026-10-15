package agent

import (
	"bytes"
	_ "embed"
	"text/template"
)

//go:embed prompts/rank_digest.md
var rankDigestTmpl string

var rankDigestPrompt = template.Must(template.New("rank_digest").Parse(rankDigestTmpl))

// DigestRanking is what the agent is given to rank one interest's
// candidates for a news-shorts digest: the items fetched from the
// interest's feeds that were not sent to the user before.
type DigestRanking struct {
	UserName string
	Date     string // ISO date of the digest, the coverage day
	Time     string // local slot time, 15:04
	Interest string
	// Rubric is the interest's rubric, in prose.
	Rubric     string
	Candidates []DigestCandidate
	// RecentStories were sent to the user lately, newest first; a candidate
	// on one of them reuses its slug.
	RecentStories []RecentStory
}

// RecentStory is a story sent lately, with the title of its item.
type RecentStory struct {
	Story string `json:"story"`
	Title string `json:"title"`
}

// DigestCandidate is one feed item.
type DigestCandidate struct {
	ID        string `json:"id"`
	Interest  string `json:"interest"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Source    string `json:"source"`              // the feed's title
	Published string `json:"published,omitempty"` // ISO date
	Content   string `json:"content"`
}

// RankDigestPrompt is the turn text that has the agent judge an interest's
// candidates; run it with OutputSchema RankDigestSchema.
func RankDigestPrompt(r DigestRanking) (string, error) {
	var b bytes.Buffer
	err := rankDigestPrompt.Execute(&b, r)
	return b.String(), err
}

// RankDigestSchema is the structured output of a ranking: for every
// candidate, its scores on the rubric's criteria, whether to include it at
// all, its category and story, and a summary.
const RankDigestSchema = `{
  "type": "object",
  "properties": {
    "judgements": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string", "description": "The candidate's id"},
          "scores": {
            "type": "array",
            "description": "One score for each criterion the rubric defines",
            "items": {
              "type": "object",
              "properties": {
                "name": {"type": "string", "description": "The criterion's name, as the rubric gives it"},
                "score": {"type": "integer", "minimum": 0, "maximum": 10}
              },
              "required": ["name", "score"],
              "additionalProperties": false
            }
          },
          "include": {"type": "boolean", "description": "Whether the item belongs in the digest at all, as the rubric decides"},
          "category": {"type": "string", "description": "One of the rubric's categories; empty when it defines none"},
          "story": {"type": "string", "description": "A 2-5 word lowercase-hyphenated slug naming the underlying story"},
          "summary": {"type": "string", "description": "One or two plain sentences: what it is and why it is worth the reader's time"}
        },
        "required": ["id", "scores", "include", "category", "story", "summary"],
        "additionalProperties": false
      }
    }
  },
  "required": ["judgements"],
  "additionalProperties": false
}`

// RankedInterest is the structured output of RankDigestSchema.
type RankedInterest struct {
	Judgements []Judgement `json:"judgements"`
}

// Judgement is the ranking of one candidate.
type Judgement struct {
	ID       string           `json:"id"`
	Scores   []CriterionScore `json:"scores"`
	Include  bool             `json:"include"`
	Category string           `json:"category"`
	Story    string           `json:"story"`
	Summary  string           `json:"summary"`
}

// CriterionScore is the score on one criterion, 0 to 10.
type CriterionScore struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

// Mean is the judgement's combined score, the mean of its scores; 0 when it
// has none.
func (j Judgement) Mean() float64 {
	if len(j.Scores) == 0 {
		return 0
	}
	sum := 0
	for _, s := range j.Scores {
		sum += s.Score
	}
	return float64(sum) / float64(len(j.Scores))
}
