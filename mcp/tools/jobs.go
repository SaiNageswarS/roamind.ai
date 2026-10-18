package tools

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxJobLinks is how many links one call may submit.
const maxJobLinks = 20

// SubmitLinksInput is submit_job_links' input.
type SubmitLinksInput struct {
	URLs []string `json:"urls" jsonschema:"the job posting links from the user's message, as they wrote them"`
}

// jobs adds the tool through which a chat turn asks the job assistant to
// draft applications. It only records the links: after the turn, the
// worker starts drafting those found in the user's own message, and posts
// the applications in the turn's thread.
func jobs(s *mcp.Server, d Deps) {
	st, userID := d.Store, d.UserID
	mcp.AddTool(s, &mcp.Tool{
		Name: "submit_job_links",
		Description: "Ask the job assistant to draft an application, a cover letter and answers to the application form's questions, " +
			"for each job posting the user linked to in their message and wants to apply to. It writes them from the user's resume, " +
			"their own person note in memory, and posts them in this thread a few minutes after your reply; the user applies themselves. " +
			"Use it only for links the user gave in this message; when it isn't clear they want to apply, ask first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SubmitLinksInput) (*mcp.CallToolResult, Done, error) {
		if err := check(len(in.URLs) > 0 && len(in.URLs) <= maxJobLinks, fmt.Sprintf("give 1 to %d links", maxJobLinks)); err != nil {
			return nil, Done{}, err
		}
		for _, raw := range in.URLs {
			u, err := url.Parse(strings.TrimSpace(raw))
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(raw) > 2048 {
				return nil, Done{}, fmt.Errorf("not a web address: %q", raw)
			}
		}
		if err := st.AddLinkRequests(ctx, userID, in.URLs); err != nil {
			return nil, Done{}, err
		}
		return nil, Done{Message: fmt.Sprintf("Submitted %d links. The job assistant posts the drafts in this thread "+
			"in a few minutes.", len(in.URLs))}, nil
	})
}
