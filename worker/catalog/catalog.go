// Package catalog is the pre-built catalog of news interests, each with its
// curated feeds and rubric. The worker writes it to the store at start (see
// store.SyncCatalog). Edit interests.json to change the interests and their
// feeds, and rubrics/*.md to change how their items are ranked.
package catalog

import (
	"embed"
	"encoding/json"
	"fmt"

	"roamind.ai/channels/store"
)

//go:embed interests.json rubrics/*.md
var files embed.FS

// Interests returns the curated interests, each with its rubric's text.
func Interests() ([]store.Interest, error) {
	b, err := files.ReadFile("interests.json")
	if err != nil {
		return nil, err
	}
	var ins []store.Interest
	if err := json.Unmarshal(b, &ins); err != nil {
		return nil, err
	}
	// In the file, rubric names the interest's rubric file.
	for i, in := range ins {
		r, err := files.ReadFile(in.Rubric)
		if err != nil {
			return nil, fmt.Errorf("%s: rubric: %w", in.Name, err)
		}
		ins[i].Rubric = string(r)
	}
	return ins, nil
}
