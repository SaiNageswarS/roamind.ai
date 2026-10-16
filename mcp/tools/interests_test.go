package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"roamind.ai/channels/feeds"
	"roamind.ai/memory/store"
	"roamind.ai/memory/store/storetest"
)

const feed = `<?xml version="1.0"?><rss version="2.0"><channel><title>RNN news</title>
<item><title>A new RNN</title><link>https://c.org/rnn</link></item></channel></rss>`

func TestInterests(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/feed" {
			w.Write([]byte(feed))
			return
		}
		w.Write([]byte("<html>a page</html>"))
	}))
	defer srv.Close()
	st := storetest.Open(t)
	for _, id := range []string{"h1", "h2"} {
		if err := st.UpsertSlackUser(ctx, store.User{ID: id, Email: id + "@x.com", SlackTeamID: "T1", SlackUserID: "U" + id}); err != nil {
			t.Fatal(err)
		}
	}
	rubric := "Score two separate judgements, 0-10.\n1. novelty -- is anything new here?\n2. rigor -- is it well supported?\nSet include false for tutorials and listicles."
	storetest.Curate(t, st,
		store.Interest{Name: "Stoic principles", Sources: []string{"https://a.org/feed"}, Rubric: rubric},
		store.Interest{Name: "Healthy eating", Sources: []string{"https://b.org/feed"}, Rubric: rubric})
	if err := st.FollowInterest(ctx, "h2", "Healthy eating"); err != nil {
		t.Fatal(err)
	}

	st1, st2 := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "roamind", Version: "v1"}, nil)
	// The feeds are on loopback, which feeds.New refuses.
	Register(server, Deps{Store: st, Feeds: &feeds.Client{HTTP: srv.Client()}, UserID: "h1"})
	if _, err := server.Connect(ctx, st1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v1"}, nil).Connect(ctx, st2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if res.IsError {
			b, _ = json.Marshal(res.Content)
		}
		return string(b), res.IsError
	}
	expect := func(name string, args map[string]any, wantErr bool, want string) {
		t.Helper()
		out, isErr := call(name, args)
		if isErr != wantErr || !strings.Contains(out, want) {
			t.Errorf("%s %v: %s (error %v), want %q", name, args, out, isErr, want)
		}
	}

	// The catalog, with what this user follows: nothing yet, though h2
	// follows Healthy eating.
	if out, _ := call("list_interests", nil); strings.Contains(out, `"following"`) || !strings.Contains(out, `"interest":"Stoic principles"`) {
		t.Fatalf("list: %s", out)
	}
	expect("follow_interest", map[string]any{"interest": "stoic PRINCIPLES"}, false, "following")
	expect("follow_interest", map[string]any{"interest": "Knitting"}, true, "not in the catalog")

	good := []string{srv.URL + "/feed"}
	expect("add_interest", map[string]any{"interest": "Recurrent neural networks", "sources": []string{srv.URL + "/page"}, "rubric": rubric}, true, "not a working RSS or Atom feed")
	expect("add_interest", map[string]any{"interest": "Recurrent neural networks", "sources": good, "rubric": "novelty"}, true, "write the rubric in prose")
	expect("add_interest", map[string]any{"interest": "Healthy Eating", "sources": good, "rubric": rubric}, true, "already in the catalog")
	expect("add_interest", map[string]any{"interest": "Recurrent neural networks", "sources": good, "rubric": rubric}, false, "followed it")
	// It is h1's own: h2's catalog doesn't have it.
	if cat, _ := st.Catalog(ctx, "h2"); len(cat) != 2 {
		t.Fatalf("h2's catalog = %+v", cat)
	}

	expect("set_interest_status", map[string]any{"interest": "Recurrent neural networks", "status": "muted"}, false, "muted")
	expect("set_interest_status", map[string]any{"interest": "Healthy eating", "status": "muted"}, true, "doesn't follow")
	expect("set_interest_status", map[string]any{"interest": "Stoic principles", "status": "paused"}, true, "status must be one of")

	out, _ := call("list_interests", nil)
	for _, want := range []string{`"interest":"Recurrent neural networks"`, `"following":"muted"`, `"following":"active"`, `"own":true`} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %s: %s", want, out)
		}
	}
	// The other user's follows are untouched.
	if uis, _ := st.UserInterests(ctx, "h2"); len(uis) != 1 || uis[0].Status != store.StatusActive {
		t.Fatalf("h2 = %+v", uis)
	}
}
