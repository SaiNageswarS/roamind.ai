package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"roamind.ai/memory/store"
	"roamind.ai/memory/store/storetest"
)

func TestItems(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	for _, id := range []string{"h1", "h2"} {
		if err := st.UpsertSlackUser(ctx, store.User{ID: id, Email: id + "@x.com", SlackTeamID: "T1", SlackUserID: "U" + id}); err != nil {
			t.Fatal(err)
		}
	}
	connect := func(userID string) *mcp.ClientSession {
		t.Helper()
		st1, st2 := mcp.NewInMemoryTransports()
		server := mcp.NewServer(&mcp.Implementation{Name: "roamind", Version: "v1"}, nil)
		if err := Register(server, Deps{Store: st, UserID: userID}, []string{Items}); err != nil {
			t.Fatal(err)
		}
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
	cs := connect("h1")
	call := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if res.IsError {
			b, _ = json.Marshal(res.Content)
		}
		return string(b), res.IsError
	}
	list := func(args map[string]any) ListItemsOutput {
		t.Helper()
		msg, isErr := call("list_items", args)
		var out ListItemsOutput
		if err := json.Unmarshal([]byte(msg), &out); isErr || err != nil {
			t.Fatalf("list_items(%v) = %s %v", args, msg, err)
		}
		return out
	}

	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"key": "x", "kind": "reminder", "note": "n", "title": "t"}, "kind must be one of"},
		{map[string]any{"key": "x", "kind": "event", "event_kind": "party", "note": "n", "title": "t"}, "event_kind must be one of"},
		{map[string]any{"key": "x", "kind": "task", "event_kind": "travel", "note": "n", "title": "t"}, "for events only"},
		{map[string]any{"key": "x", "kind": "task", "note": "", "title": "t"}, "are required"},
		{map[string]any{"key": "x", "kind": "event", "note": "n", "title": "t", "date": "next Friday"}, "not an ISO date"},
	} {
		if msg, isErr := call("save_item", c.args); !isErr || !strings.Contains(msg, c.want) {
			t.Errorf("save_item(%v) = %s %v", c.args, msg, isErr)
		}
	}

	if msg, isErr := call("save_item", map[string]any{"key": "Call the bank", "kind": "task", "note": "tasks/call-the-bank",
		"title": "Call the bank", "date": "2026-11-06", "remind_on": "2026-11-06"}); isErr || !strings.Contains(msg, "reminded from 2026-11-06 until it is closed") {
		t.Fatalf("save task = %s", msg)
	}
	if msg, isErr := call("save_item", map[string]any{"key": "XK7Q2P", "kind": "event", "event_kind": "stay", "note": "events/2026/hotel-lisbon",
		"title": "Hotel in Lisbon", "date": "2026-11-11", "end": "2026-11-14T11:00"}); isErr || !strings.Contains(msg, "reminded from 2026-11-10 to 2026-11-14") {
		t.Fatalf("save event = %s", msg)
	}
	if msg, isErr := call("save_item", map[string]any{"key": "Write tests", "kind": "task", "note": "tasks/roamind/write-tests",
		"title": "Write tests"}); isErr || !strings.Contains(msg, "no reminder") {
		t.Fatalf("save undated = %s", msg)
	}
	if out := list(nil); len(out.Items) != 3 || out.Items[0].Key != "Call the bank" || out.Items[1].ShowFrom != "2026-11-10" || out.Items[2].Date != "" {
		t.Fatalf("open = %+v", out)
	}
	if out := list(map[string]any{"folder": "tasks/roamind/"}); len(out.Items) != 1 || out.Items[0].Key != "Write tests" {
		t.Fatalf("goal's tasks = %+v", out)
	}
	// Basic Memory starts permalinks with the project; folders match after it.
	project, err := st.AssignMemoryProject(ctx, "h1", "bm-u-h1")
	if err != nil {
		t.Fatal(err)
	}
	if msg, isErr := call("save_item", map[string]any{"key": "Book flights", "kind": "task", "note": project + "/tasks/roamind/book-flights",
		"title": "Book flights"}); isErr {
		t.Fatalf("save prefixed = %s", msg)
	}
	for folder, want := range map[string]int{"tasks/roamind/": 2, project + "/tasks/roamind/": 1} {
		if out := list(map[string]any{"folder": folder}); len(out.Items) != want {
			t.Fatalf("goal's tasks in %s = %+v", folder, out)
		}
	}
	if msg, isErr := call("close_item", map[string]any{"key": "Book flights", "state": "cancelled"}); isErr {
		t.Fatalf("close prefixed = %s", msg)
	}
	if msg, isErr := call("list_items", map[string]any{"from": "2026-11"}); !isErr || !strings.Contains(msg, "ISO dates") {
		t.Fatalf("month = %s", msg)
	}

	if msg, isErr := call("close_item", map[string]any{"key": "write tests", "state": "done"}); isErr || !strings.Contains(msg, "is done") {
		t.Fatalf("close = %s", msg)
	}
	if msg, isErr := call("close_item", map[string]any{"key": "nothing", "state": "done"}); !isErr || !strings.Contains(msg, "no item") {
		t.Fatalf("close unknown = %s", msg)
	}
	if msg, isErr := call("close_item", map[string]any{"key": "XK7Q2P", "state": "paid"}); !isErr || !strings.Contains(msg, "state must be one of") {
		t.Fatalf("close paid = %s", msg)
	}
	if out := list(map[string]any{"key": "Write tests"}); len(out.Items) != 1 || out.Items[0].State != "done" || out.Items[0].Closed == "" {
		t.Fatalf("by key = %+v", out)
	}
	if out := list(map[string]any{"from": "2026-11-07", "to": "2026-11-30"}); len(out.Items) != 1 || out.Items[0].Key != "XK7Q2P" {
		t.Fatalf("range = %+v", out)
	}

	// Pinned to its user.
	cs = connect("h2")
	if out := list(nil); len(out.Items) != 0 {
		t.Fatalf("h2 sees %+v", out)
	}
	if _, isErr := call("close_item", map[string]any{"key": "XK7Q2P", "state": "done"}); !isErr {
		t.Fatal("h2 closed h1's item")
	}
}
