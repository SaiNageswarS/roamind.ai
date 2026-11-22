package workflows

import (
	"context"
	"os"
	"strings"
	"testing"

	"roamind.ai/channels/identity"
	"roamind.ai/channels/slack"
	"roamind.ai/memory/store"
)

func TestProcessFinanceChannel(t *testing.T) {
	ctx := context.Background()
	d, fs, argsLog := setup(t)
	withMemory(t, &d)
	for _, ts := range []string{"0.5", "1.0"} {
		if _, err := d.Process(ctx, "r0", mention("Ev"+ts, "U1", ts, "")); err != nil {
			t.Fatal(err)
		}
	}
	owner := identity.UserID("owner@x.com")
	if _, _, err := d.Store.EnsureFinAccount(ctx, owner, "zerodha", "XX1234", "INR"); err != nil {
		t.Fatal(err)
	}
	out, err := d.financeChannel(ctx, owner)
	if err != nil || !strings.HasPrefix(out.Created, "finance-") {
		t.Fatalf("finance channel = %+v %v", out, err)
	}
	ch, _ := d.Store.Channel(ctx, owner, store.KindFinance, "")
	if again, err := d.financeChannel(ctx, owner); err != nil || again.Created != "" {
		t.Errorf("second run = %+v %v", again, err)
	}
	n := 0
	for _, c := range fs.created {
		if strings.HasPrefix(c, "finance-") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("finance channels created = %d of %v", n, fs.created)
	}

	os.Remove(argsLog)
	turn, err := d.Process(ctx, "r", slack.Envelope{TeamID: "T1", EventID: "Ev9", Event: slack.Event{
		Type: "message", ChannelType: "group", User: "U1", Text: "how are my stocks?", Channel: ch, TS: "3.0"}})
	if err != nil || turn.Skipped != "" {
		t.Fatalf("turn = %+v %v", turn, err)
	}
	args, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(args), "finance channel") || !strings.Contains(string(args), "a1 zerodha XX1234") {
		t.Errorf("the finance channel isn't in the prompt: %s", args)
	}
}
