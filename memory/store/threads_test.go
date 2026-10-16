package store

import (
	"context"
	"testing"
)

func TestThreads(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.UpsertSlackUser(ctx, User{ID: "h1", Email: "a@x.com"}); err != nil {
		t.Fatal(err)
	}
	th := Thread{ChannelID: "C1", ThreadTS: "1.0", UserID: "h1", ClaudeSessionID: "s1"}
	if err := s.SaveThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	th.ClaudeSessionID = "s2"
	if err := s.SaveThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetThread(ctx, "C1", "1.0")
	if err != nil || got == nil || got.ClaudeSessionID != "s2" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if got, _ := s.GetThread(ctx, "C1", "2.0"); got != nil {
		t.Fatal("want nil")
	}
}
