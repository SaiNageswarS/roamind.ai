package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"roamind.ai/memory/store"
)

// CatalogEntry is a catalog interest as list_interests shows it.
type CatalogEntry struct {
	Interest string   `json:"interest"`
	Sources  []string `json:"sources"`
	Rubric   string   `json:"rubric"`
	// Following is the user's status for it: active, muted, or empty when
	// they don't follow it.
	Following string `json:"following,omitempty"`
	// Own is set for interests the user added; the others are curated.
	Own bool `json:"own,omitempty"`
}

// ListOutput is list_interests' result.
type ListOutput struct {
	Catalog []CatalogEntry `json:"catalog"`
}

// FollowInput names a catalog interest.
type FollowInput struct {
	Interest string `json:"interest" jsonschema:"the catalog interest, as list_interests shows it"`
}

// AddInput is add_interest's input.
type AddInput struct {
	Interest string   `json:"interest" jsonschema:"the topic, in a few words"`
	Sources  []string `json:"sources" jsonschema:"two to four RSS or Atom feed URLs of reputable sources on the topic"`
	Rubric   string   `json:"rubric" jsonschema:"the rubric in prose: two to four named criteria scored 0 to 10, each with what its score bands mean, and when an item is not to be included at all"`
}

// StatusInput is set_interest_status' input.
type StatusInput struct {
	Interest string `json:"interest" jsonschema:"an interest the user follows"`
	Status   string `json:"status" jsonschema:"muted to pause its items, active to resume them"`
}

// maxRubric caps a rubric's length; the longest curated one is about 5,000
// characters.
const maxRubric = 8000

// Done is the result of a write.
type Done struct {
	Message string `json:"message"`
}

// interests adds the tools through which the user follows interests from
// the catalog and adds new ones; the workflows read them from the store.
func interests(s *mcp.Server, d Deps) {
	st, userID := d.Store, d.UserID
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_interests",
		Description: "List the user's catalog of news interests, the curated ones and those they added, each with its feeds, " +
			"its rubric and whether the user follows it. The user's news-shorts digests come from the feeds of the interests they follow.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListOutput, error) {
		out := ListOutput{Catalog: []CatalogEntry{}}
		catalog, err := st.Catalog(ctx, userID)
		if err != nil {
			return nil, out, err
		}
		uis, err := st.UserInterests(ctx, userID)
		if err != nil {
			return nil, out, err
		}
		following := map[string]string{}
		for _, ui := range uis {
			following[strings.ToLower(ui.Interest)] = ui.Status
		}
		for _, in := range catalog {
			out.Catalog = append(out.Catalog, CatalogEntry{Interest: in.Name, Sources: in.Sources, Rubric: in.Rubric,
				Following: following[strings.ToLower(in.Name)], Own: !in.Curated()})
		}
		return nil, out, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "follow_interest",
		Description: "Follow a catalog interest, so the user's digests include its items. Following a muted interest resumes it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in FollowInput) (*mcp.CallToolResult, Done, error) {
		err := st.FollowInterest(ctx, userID, strings.TrimSpace(in.Interest))
		if errors.Is(err, store.ErrNoInterest) {
			return nil, Done{}, fmt.Errorf("%q is not in the catalog; list_interests shows it, add_interest adds to it", in.Interest)
		}
		return nil, Done{Message: "following " + in.Interest}, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "add_interest",
		Description: "Add an interest the user's catalog lacks, with its feeds and rubric, and follow it. " +
			"It is the user's own: other users don't see it. Every feed is fetched and must be a working RSS or Atom feed.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in AddInput) (*mcp.CallToolResult, Done, error) {
		in.Interest = strings.TrimSpace(in.Interest)
		if err := check(in.Interest != "", "interest is required"); err != nil {
			return nil, Done{}, err
		}
		if err := check(len(in.Sources) >= 1 && len(in.Sources) <= 4, "give one to four feeds"); err != nil {
			return nil, Done{}, err
		}
		in.Rubric = strings.TrimSpace(in.Rubric)
		if err := check(len(in.Rubric) >= 80 && len(in.Rubric) <= maxRubric,
			fmt.Sprintf("write the rubric in prose, in 80 to %d characters", maxRubric)); err != nil {
			return nil, Done{}, err
		}
		if have, err := st.CatalogInterest(ctx, userID, in.Interest); err != nil || have != nil {
			if err == nil {
				err = fmt.Errorf("%q is already in the catalog; follow it with follow_interest", have.Name)
			}
			return nil, Done{}, err
		}
		for _, src := range in.Sources {
			if u, err := url.Parse(src); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return nil, Done{}, fmt.Errorf("%q is not a feed URL", src)
			}
			entries, err := d.Feeds.Fetch(ctx, src)
			if err != nil {
				return nil, Done{}, fmt.Errorf("%s is not a working RSS or Atom feed: %v", src, err)
			}
			if len(entries) == 0 {
				return nil, Done{}, fmt.Errorf("%s has no items", src)
			}
		}
		if err := st.AddInterest(ctx, userID, store.Interest{Name: in.Interest, Sources: in.Sources, Rubric: in.Rubric}); err != nil {
			return nil, Done{}, err
		}
		if err := st.FollowInterest(ctx, userID, in.Interest); err != nil {
			return nil, Done{}, err
		}
		return nil, Done{Message: "added " + in.Interest + " to the user's catalog and followed it"}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_interest_status",
		Description: "Mute an interest the user follows to pause its items, or make it active again.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in StatusInput) (*mcp.CallToolResult, Done, error) {
		if err := oneOf("status", in.Status, store.StatusActive, store.StatusMuted); err != nil {
			return nil, Done{}, err
		}
		ok, err := st.SetInterestStatus(ctx, userID, strings.TrimSpace(in.Interest), in.Status)
		if err != nil {
			return nil, Done{}, err
		}
		if !ok {
			return nil, Done{}, fmt.Errorf("the user doesn't follow %q; list_interests shows what they follow", in.Interest)
		}
		return nil, Done{Message: in.Interest + " is " + in.Status}, nil
	})
}
