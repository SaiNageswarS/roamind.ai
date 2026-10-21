package store

import (
	"context"
	"testing"
)

func TestChannels(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, u := range []User{
		{ID: "h1", Email: "a@x.com", SlackTeamID: "T1", SlackUserID: "U1"},
		{ID: "h2", Email: "b@x.com", SlackTeamID: "T1", SlackUserID: "U2"},
	} {
		if err := s.UpsertSlackUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if c, err := s.ChannelOf(ctx, "G1", "U1"); err != nil || c != nil {
		t.Fatalf("before: c=%v err=%v", c, err)
	}
	goal := Channel{UserID: "h1", Kind: KindGoal, Ref: "p/goals/jax", Title: "Learn JAX", ChannelID: "G1"}
	if err := s.SaveChannel(ctx, goal); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannel(ctx, goal); err == nil {
		t.Fatal("duplicate goal channel saved")
	}
	// A channel has one purpose for a user.
	if err := s.SaveChannel(ctx, Channel{UserID: "h1", Kind: KindNews, ChannelID: "G1"}); err == nil {
		t.Fatal("second purpose for the same channel and user saved")
	}
	if err := s.SaveChannel(ctx, Channel{UserID: "h1", Kind: KindNews, ChannelID: "N1"}); err != nil {
		t.Fatal(err)
	}
	goal.SlackUserID = "U1"
	if got, err := s.ChannelOf(ctx, "G1", "U1"); err != nil || got == nil || *got != goal {
		t.Fatalf("goal: got=%+v err=%v", got, err)
	}
	if got, err := s.ChannelOf(ctx, "N1", "U1"); err != nil || got == nil || got.Kind != KindNews || got.UserID != "h1" {
		t.Fatalf("news: got=%+v err=%v", got, err)
	}
	// Channels are routed by their owner only.
	for _, ch := range []string{"G1", "N1"} {
		if c, err := s.ChannelOf(ctx, ch, "U2"); err != nil || c != nil {
			t.Fatalf("%s for non-member: c=%v err=%v", ch, c, err)
		}
	}
	// Another user shares the goal channel, with a goal in their own memory.
	if err := s.SaveChannel(ctx, Channel{UserID: "h2", Kind: KindGoal, Ref: "q/goals/jax", Title: "JAX too", ChannelID: "G1"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ChannelOf(ctx, "G1", "U2"); err != nil || got == nil || got.UserID != "h2" || got.Ref != "q/goals/jax" {
		t.Fatalf("shared: got=%+v err=%v", got, err)
	}
	if ch, err := s.Channel(ctx, "h1", KindGoal, "p/goals/jax"); err != nil || ch != "G1" {
		t.Fatalf("Channel(goal) = %q %v", ch, err)
	}
	if ch, err := s.Channel(ctx, "h1", KindNews, ""); err != nil || ch != "N1" {
		t.Fatalf("Channel(news) = %q %v", ch, err)
	}
	if ch, err := s.Channel(ctx, "h2", KindNews, ""); err != nil || ch != "" {
		t.Fatalf("Channel(none) = %q %v", ch, err)
	}
	goals, err := s.Channels(ctx, KindGoal)
	if err != nil || len(goals) != 2 || goals[0] != goal || goals[1].UserID != "h2" {
		t.Fatalf("goals=%+v err=%v", goals, err)
	}
	if news, err := s.Channels(ctx, KindNews); err != nil || len(news) != 1 || news[0].ChannelID != "N1" {
		t.Fatalf("news=%+v err=%v", news, err)
	}
}
