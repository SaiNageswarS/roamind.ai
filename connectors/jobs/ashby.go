package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
)

// ashby is Ashby's job boards, jobs.ashbyhq.com. Its API has no posting on
// its own: the company's board lists every job with its description.
type ashby struct{}

// MaxBoard caps an Ashby board; it is read as a stream, so it isn't held
// in memory.
const MaxBoard = 64 << 20

func (ashby) Match(u *url.URL) (Ref, bool) {
	if hostOf(u) != "jobs.ashbyhq.com" {
		return Ref{}, false
	}
	m := idPath.FindStringSubmatch(u.Path)
	if m == nil {
		return Ref{}, false
	}
	return Ref{Source: Ashby, URL: "https://jobs.ashbyhq.com/" + m[1] + "/" + m[2], host: "api.ashbyhq.com", board: m[1], id: m[2]}, true
}

func (ashby) Posting(ctx context.Context, c *Client, ref Ref) (*Posting, error) {
	type job struct {
		ID               string `json:"id"`
		Title            string `json:"title"`
		Location         string `json:"location"`
		PublishedAt      string `json:"publishedAt"`
		JobURL           string `json:"jobUrl"`
		ApplyURL         string `json:"applyUrl"`
		DescriptionPlain string `json:"descriptionPlain"`
	}
	resp, err := c.get(ctx, "https://"+ref.host+"/posting-api/job-board/"+url.PathEscape(ref.board), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// {"jobs": [...], ...}: the jobs are decoded one at a time, up to ours.
	dec := json.NewDecoder(io.LimitReader(resp.Body, MaxBoard))
	if err := seekArray(dec, "jobs"); err != nil {
		return nil, fmt.Errorf("ashby board: %w", err)
	}
	for dec.More() {
		var j job
		if err := dec.Decode(&j); err != nil {
			return nil, fmt.Errorf("ashby board: %w", err)
		}
		if j.ID == ref.id {
			return &Posting{URL: j.JobURL, ApplyURL: j.ApplyURL, Title: j.Title, Company: ref.board,
				Location: j.Location, Posted: isoDate(j.PublishedAt), Description: j.DescriptionPlain}, nil
		}
	}
	return nil, ErrGone
}

// seekArray reads dec up to the start of the array under key in the
// top-level object, skipping the values before it.
func seekArray(dec *json.Decoder, key string) error {
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return fmt.Errorf("not an object: %v", err)
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if t == key {
			if t, err := dec.Token(); err != nil || t != json.Delim('[') {
				return fmt.Errorf("%s is not an array: %v", key, err)
			}
			return nil
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
	}
	return fmt.Errorf("no %s", key)
}
