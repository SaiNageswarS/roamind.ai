package workflows

import (
	"context"
	"strings"
	"testing"

	slackapi "github.com/slack-go/slack"

	"roamind.ai/channels/slack"
)

func fileSharedEvent(channel string) slack.Envelope {
	return slack.Envelope{TeamID: "T1", EventID: "EvF",
		Authorizations: []slack.Authorization{{UserID: "UBOT", IsBot: true}},
		Event:          slack.Event{Type: "file_shared", FileID: "F1", UserID: "U1", ChannelID: channel}}
}

func sharedMsg(ts, text string) slackapi.Message {
	return slackapi.Message{Msg: slackapi.Msg{Timestamp: ts, User: "U1", Text: text,
		Files: []slackapi.File{{ID: "F1", Name: "a.pdf", Mimetype: "application/pdf", Size: 3, URLPrivateDownload: "https://files.slack.com/a.pdf"}}}}
}

func TestFileSharedStartsMissingTurn(t *testing.T) {
	ctx := context.Background()
	d, fs, _ := setup(t)
	fs.shares = slackapi.Share{Private: map[string][]slackapi.ShareFileInfo{"G1": {{Ts: "5.0"}}}}
	fs.replies = []slackapi.Message{sharedMsg("5.0", "Import")}

	var started []slack.Envelope
	run := func(_ context.Context, env slack.Envelope) error { started = append(started, env); return nil }
	out, err := d.fileShared(ctx, fileSharedEvent("G1"), 0, run)
	if err != nil || len(out.Started) != 1 || len(started) != 1 {
		t.Fatalf("out=%+v err=%v started=%d", out, err, len(started))
	}
	ev := started[0].Event
	if ev.Type != "message" || ev.ChannelType != "group" || ev.Channel != "G1" || ev.TS != "5.0" ||
		ev.User != "U1" || len(ev.Files) != 1 || ev.Files[0].ID != "F1" || !started[0].IsChat() {
		t.Fatalf("envelope = %+v", started[0])
	}

	// Once the message had its turn, the fallback starts none.
	if _, err := d.Store.MarkEvent(ctx, messageKey("G1", "5.0")); err != nil {
		t.Fatal(err)
	}
	started = nil
	out, err = d.fileShared(ctx, fileSharedEvent("G1"), 0, run)
	if err != nil || len(started) != 0 || len(out.Handled) != 1 {
		t.Fatalf("handled message restarted: out=%+v err=%v", out, err)
	}
}

func TestFileSharedChannelTypes(t *testing.T) {
	ctx := context.Background()
	d, fs, _ := setup(t)
	var got slack.Envelope
	run := func(_ context.Context, env slack.Envelope) error { got = env; return nil }

	fs.shares = slackapi.Share{Private: map[string][]slackapi.ShareFileInfo{"D1": {{Ts: "6.0"}}}}
	fs.replies = []slackapi.Message{sharedMsg("6.0", "Import")}
	if _, err := d.fileShared(ctx, fileSharedEvent("D1"), 0, run); err != nil || got.Event.ChannelType != "im" {
		t.Fatalf("dm: %+v err=%v", got.Event, err)
	}

	// In a public channel only a mention of the bot is for the agent.
	fs.shares = slackapi.Share{Public: map[string][]slackapi.ShareFileInfo{"C1": {{Ts: "7.0", ThreadTs: "6.5"}}}}
	fs.replies = []slackapi.Message{sharedMsg("6.5", "root"), sharedMsg("7.0", "<@UBOT> import")}
	got = slack.Envelope{}
	if _, err := d.fileShared(ctx, fileSharedEvent("C1"), 0, run); err != nil ||
		got.Event.Type != "app_mention" || got.Event.TS != "7.0" {
		t.Fatalf("mention: %+v err=%v", got.Event, err)
	}
	fs.replies = []slackapi.Message{sharedMsg("7.0", "no mention")}
	got = slack.Envelope{}
	if out, err := d.fileShared(ctx, fileSharedEvent("C1"), 0, run); err != nil || got.Event.TS != "" || len(out.Started) != 0 {
		t.Fatalf("public message without mention started a turn: %+v", out)
	}
}

func TestProcessSkipsDuplicateMessage(t *testing.T) {
	ctx := context.Background()
	d, _, _ := setup(t)
	if out, err := d.Process(ctx, "r1", mention("Ev1", "U1", "1.0", "")); err != nil || out.Skipped != "" {
		t.Fatalf("first: %+v %v", out, err)
	}
	// The same message under another event id, as the file_shared fallback
	// delivers it.
	if out, _ := d.Process(ctx, "r2", mention("", "U1", "1.0", "")); !strings.HasPrefix(out.Skipped, "duplicate message") {
		t.Fatalf("not skipped: %+v", out)
	}
}
