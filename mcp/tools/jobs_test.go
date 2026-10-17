package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"roamind.ai/memory/store"
	"roamind.ai/memory/store/storetest"
)

func TestSubmitJobLinks(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	if err := st.UpsertSlackUser(ctx, store.User{ID: "h1", Email: "h1@x.com", SlackTeamID: "T1", SlackUserID: "U1"}); err != nil {
		t.Fatal(err)
	}
	connect := func(jobs bool) *mcp.ClientSession {
		t.Helper()
		st1, st2 := mcp.NewInMemoryTransports()
		server := mcp.NewServer(&mcp.Implementation{Name: "roamind", Version: "v1"}, nil)
		Register(server, Deps{Store: st, UserID: "h1", Jobs: jobs})
		if _, err := server.Connect(ctx, st1, nil); err != nil {
			t.Fatal(err)
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v1"}, nil).Connect(ctx, st2, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cs.Close() })
		return cs
	}

	// Without the job assistant there is no such tool.
	tools, err := connect(false).ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "submit_job_links" {
			t.Fatal("submit_job_links without --jobs")
		}
	}

	cs := connect(true)
	call := func(urls ...string) (string, bool) {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "submit_job_links", Arguments: map[string]any{"urls": urls}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if res.IsError {
			b, _ = json.Marshal(res.Content)
		}
		return string(b), res.IsError
	}
	const link = "https://jobs.lever.co/acme/6ed76ce8-4156-4b60-b120-403538bd66cd"
	// A user who never signed in to the web app is told to.
	if msg, isErr := call(link); isErr || !strings.Contains(msg, "hasn't signed in") {
		t.Fatalf("no profile = %s %v", msg, isErr)
	}
	if got, _ := st.TakeLinkRequests(ctx, "h1"); len(got) != 0 {
		t.Fatalf("recorded without a profile: %v", got)
	}
	if err := st.SaveWebProfile(ctx, "h1", "uid1"); err != nil {
		t.Fatal(err)
	}
	if msg, isErr := call("ftp://x/1"); !isErr || !strings.Contains(msg, "not a web address") {
		t.Fatalf("ftp = %s %v", msg, isErr)
	}
	if msg, isErr := call(link, "https://acme.com/careers/2"); isErr || !strings.Contains(msg, "Submitted 2 links") {
		t.Fatalf("submit = %s %v", msg, isErr)
	}
	if got, _ := st.TakeLinkRequests(ctx, "h1"); !reflect.DeepEqual(got, []string{link, "https://acme.com/careers/2"}) &&
		!reflect.DeepEqual(got, []string{"https://acme.com/careers/2", link}) {
		t.Fatalf("requests = %v", got)
	}
}
