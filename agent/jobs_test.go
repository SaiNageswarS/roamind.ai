package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJobPrompts(t *testing.T) {
	for name, schema := range map[string]string{"found": FoundJobsSchema, "rank": RankJobsSchema, "tailor": TailorApplicationSchema} {
		if !json.Valid([]byte(schema)) {
			t.Errorf("%s schema is not JSON", name)
		}
	}
	s := JobSearch{UserName: "Sai", Date: "2026-10-17", Roles: []string{"ML Engineer"}, Regions: []string{"Berlin"},
		Max: 10}
	web, err := FindJobsPrompt(s)
	if err != nil || !strings.Contains(web, "- ML Engineer") || !strings.Contains(web, "up to 10 postings") || strings.Contains(web, "<notes>") {
		t.Errorf("find: %v\n%s", err, web)
	}
	rank, err := RankJobsPrompt(JobRanking{UserName: "Sai", Roles: s.Roles, Regions: s.Regions, Resume: "CV text",
		Candidates: []JobCandidate{{ID: "c1", Title: "ML Engineer", Company: "Acme", Location: "Berlin", Description: "Build models"}}})
	if err != nil || !strings.Contains(rank, `<posting id="c1">`+"\nML Engineer, Acme (Berlin)\nBuild models") {
		t.Errorf("rank: %v\n%s", err, rank)
	}
	// Links from Slack come with no roles or regions.
	rank, err = RankJobsPrompt(JobRanking{UserName: "Sai", Resume: "CV text", Candidates: []JobCandidate{{ID: "c1", Title: "ML Engineer"}}})
	if err != nil || strings.Contains(rank, "Roles they want") || strings.Contains(rank, "Regions") || !strings.Contains(rank, "picked these postings themselves") {
		t.Errorf("rank without roles: %v\n%s", err, rank)
	}
	t1 := Tailoring{UserName: "Sai", UserEmail: "sai@x.com", Resume: "CV text", Posting: JobCandidate{Title: "ML Engineer"},
		Questions: []FormQuestion{{Label: "Relocate?", Required: true, Kind: "select", Options: []string{"Yes", "No"}}}}
	tailor, err := TailorApplicationPrompt(t1)
	if err != nil || !strings.Contains(tailor, "- Relocate? (required), select, one of: Yes; No") || !strings.Contains(tailor, "sai@x.com") {
		t.Errorf("tailor: %v\n%s", err, tailor)
	}
	t1.Questions = nil
	if tailor, err = TailorApplicationPrompt(t1); err != nil || !strings.Contains(tailor, "questions are not published") {
		t.Errorf("tailor without questions: %v\n%s", err, tailor)
	}
}
