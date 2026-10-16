package workflows

import (
	"context"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/channels/slack"
	"roamind.ai/memory/store"
)

// NewsReactionName is the task that records reactions to digests; replies
// in a digest's thread are recorded by chat-turn.
const NewsReactionName = "news-reaction"

// FeedbackOutput is the result of a feedback task.
type FeedbackOutput struct {
	Skipped string `json:"skipped,omitempty"`
	Digest  int64  `json:"digest,omitempty"`
	Value   int    `json:"value,omitempty"`
}

// NewsReaction declares the task that records reactions to digests.
func NewsReaction(c *hatchet.Client, d Deps) *hatchet.StandaloneTask {
	return c.NewStandaloneTask(NewsReactionName, func(ctx hatchet.Context, env slack.Envelope) (FeedbackOutput, error) {
		return d.RecordReaction(ctx, env)
	},
		hatchet.WithWorkflowEvents(slack.ReactionEventKey),
		hatchet.WithWorkflowDescription("Record a reaction to a news digest"),
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(30*time.Second),
	)
}

// RecordReaction records the digest owner's reaction to their digest.
func (d Deps) RecordReaction(ctx context.Context, env slack.Envelope) (FeedbackOutput, error) {
	var out FeedbackOutput
	ev := env.Event
	if ev.Type != "reaction_added" || ev.Item == nil || ev.Item.Type != "message" {
		out.Skipped = "not a reaction to a message"
		return out, nil
	}
	dg, why, err := d.ownDigest(ctx, env.TeamOf(), ev.User, ev.Item.Channel, ev.Item.TS)
	if err != nil || dg == nil {
		out.Skipped = why
		return out, err
	}
	if env.EventID != "" {
		fresh, err := d.Store.MarkEvent(ctx, env.EventID)
		if err != nil || !fresh {
			out.Skipped = "duplicate event " + env.EventID
			return out, err
		}
	}
	out.Digest, out.Value = dg.ID, ReactionValue(ev.Reaction)
	return out, d.Store.SaveSignal(ctx, store.Signal{UserID: dg.UserID, DigestID: dg.ID, Kind: "reaction", Value: out.Value})
}

// ownDigest returns the digest posted as channel/ts when the Slack user is
// the one it was posted to; otherwise nil and why not.
func (d Deps) ownDigest(ctx context.Context, team, slackUser, channel, ts string) (*store.Digest, string, error) {
	dg, err := d.Store.DigestByTS(ctx, channel, ts)
	if err != nil || dg == nil {
		return nil, "not a digest", err
	}
	u, err := d.Store.UserBySlack(ctx, team, slackUser)
	if err != nil || u == nil || u.ID != dg.UserID {
		return nil, "not the digest's owner", err
	}
	return dg, "", nil
}

var (
	positiveReactions = map[string]bool{
		"+1": true, "thumbsup": true, "heart": true, "heart_eyes": true, "orange_heart": true,
		"yellow_heart": true, "green_heart": true, "blue_heart": true, "purple_heart": true,
		"fire": true, "star": true, "star2": true, "star-struck": true, "clap": true, "bulb": true,
		"raised_hands": true, "tada": true, "100": true, "sparkles": true, "smile": true,
		"slightly_smiling_face": true, "grinning": true, "pray": true, "muscle": true,
		"white_check_mark": true, "heavy_check_mark": true, "brain": true, "exploding_head": true,
	}
	negativeReactions = map[string]bool{
		"-1": true, "thumbsdown": true, "x": true, "no_entry": true, "no_entry_sign": true,
		"confused": true, "disappointed": true, "face_with_rolling_eyes": true, "zzz": true,
		"sleeping": true, "yawning_face": true, "unamused": true, "expressionless": true,
	}
)

// ReactionValue maps a reaction's emoji name to +1, -1 or 0, ignoring its
// skin tone.
func ReactionValue(name string) int {
	name, _, _ = strings.Cut(name, "::")
	switch {
	case positiveReactions[name]:
		return 1
	case negativeReactions[name]:
		return -1
	}
	return 0
}
