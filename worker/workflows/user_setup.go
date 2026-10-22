package workflows

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/memory/store"
	"roamind.ai/memory/store/notes"
)

// UserSetupName is the user-setup workflow, which sets up a new user:
//
//	memory-project → bm-setup → news-channel, inbox-channel
//
// memory-project gives the user their Basic Memory project name; bm-setup
// creates that project (its bm config and schema notes); news-channel
// creates their private news-shorts channel and has them follow the
// catalog's default interests; inbox-channel creates their private inbox
// channel, for their inbox digests. Each task needs its parent's work, and
// each is safe to repeat, so a run for a user who is set up changes
// nothing. Nothing waits for it: chat-turn starts it when it sees a user
// who is not set up, news-shorts for users without a news channel and
// inbox-digests for users without an inbox channel.
const UserSetupName = "user-setup"

// UserSetupInput names the user to set up.
type UserSetupInput struct {
	UserID string `json:"user_id"`
}

// memoryProject is the result of memory-project and bm-setup.
type memoryProject struct {
	Project string `json:"project"`
}

// InboxChannelOutput is inbox-channel's result.
type InboxChannelOutput struct {
	Created string `json:"created,omitempty"` // channel name
}

// NewsChannelOutput is news-channel's result.
type NewsChannelOutput struct {
	Created string   `json:"created,omitempty"` // channel name
	Seeded  []string `json:"seeded,omitempty"`  // default interests followed
}

// UserSetup declares the user-setup workflow. A user has one run at a
// time, so they never get two projects or channels; runs started while one
// is in flight are cancelled, since it does their work.
func UserSetup(c *hatchet.Client, d Deps) *hatchet.Workflow {
	maxRuns := int32(1)
	strategy := hatchet.CancelNewest
	wf := c.NewWorkflow(UserSetupName,
		hatchet.WithWorkflowDescription("Set up a new user: memory project, Basic Memory, news and inbox channels"),
		hatchet.WithWorkflowConcurrency(hatchet.Concurrency{
			Expression:    "input.user_id",
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}),
	)
	project := wf.NewTask("memory-project", func(ctx hatchet.Context, in UserSetupInput) (memoryProject, error) {
		return d.assignProject(ctx, in.UserID)
	},
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(30*time.Second),
	)
	bmSetup := wf.NewTask("bm-setup", func(ctx hatchet.Context, _ UserSetupInput) (memoryProject, error) {
		var p memoryProject
		if err := ctx.ParentOutput(project, &p); err != nil {
			return p, err
		}
		return d.setUpMemory(p)
	},
		hatchet.WithParents(project),
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(30*time.Second),
	)
	wf.NewTask("news-channel", func(ctx hatchet.Context, in UserSetupInput) (NewsChannelOutput, error) {
		return d.SyncNews(ctx, in.UserID)
	},
		hatchet.WithParents(bmSetup),
		hatchet.WithRetries(3),
		hatchet.WithExecutionTimeout(2*time.Minute),
	)
	wf.NewTask("inbox-channel", func(ctx hatchet.Context, in UserSetupInput) (InboxChannelOutput, error) {
		return d.SyncInbox(ctx, in.UserID)
	},
		hatchet.WithParents(bmSetup),
		hatchet.WithRetries(3),
		hatchet.WithExecutionTimeout(2*time.Minute),
	)
	return wf
}

// assignProject gives the user their Basic Memory project name, once.
func (d Deps) assignProject(ctx context.Context, userID string) (memoryProject, error) {
	u, err := d.Store.UserByID(ctx, userID)
	if err != nil {
		return memoryProject{}, err
	}
	if u == nil {
		return memoryProject{}, fmt.Errorf("no user %s", userID)
	}
	p, err := d.Store.AssignMemoryProject(ctx, u.ID, notes.ProjectBase(u.DisplayName))
	return memoryProject{Project: p}, err
}

// setUpMemory creates the user's Basic Memory project.
func (d Deps) setUpMemory(p memoryProject) (memoryProject, error) {
	return p, d.Memory.Setup(p.Project)
}

// SyncNews creates the user's news channel when they have none, invites
// them to it, and seeds the default interests they lack. The invite is
// repeated for a channel that exists, so a retry after a failed invite
// still adds them.
func (d Deps) SyncNews(ctx context.Context, userID string) (NewsChannelOutput, error) {
	var out NewsChannelOutput
	u, err := d.Store.UserByID(ctx, userID)
	if err != nil || u == nil || u.MemoryProject == "" || u.SlackUserID == "" {
		return out, err
	}
	defaults, err := d.Store.DefaultInterests(ctx)
	if err != nil {
		return out, err
	}
	names := make([]string, len(defaults))
	for i, in := range defaults {
		names[i] = in.Name
	}
	c := store.Channel{Kind: store.KindNews}
	if _, out.Created, err = d.openChannel(ctx, u, c, NewsChannelName(u.DisplayName), d.newsIntro(names)); err != nil {
		return out, err
	}
	if out.Seeded, err = d.Store.FollowDefaults(ctx, u.ID); err != nil {
		return out, err
	}
	if out.Created != "" {
		slog.Info("news channel created", "user", u.ID, "channel", out.Created)
	}
	return out, nil
}

// newsIntro introduces the channel, with the interests the user starts
// with: the catalog's defaults.
func (d Deps) newsIntro(defaults []string) string {
	return fmt.Sprintf("This is your news-shorts channel. Every day at %s (%s) I'll post a few short, uplifting "+
		"pieces from trusted sources on your interests. You start with: %s.\n\n"+
		"Tell me here what else you're curious about, or what you'd like more or less of, no @mention needed. "+
		"React to a digest with 👍 or 👎, or reply in its thread; I use both to choose what comes next.",
		newsTimes(), d.Location, strings.Join(defaults, ", "))
}

// SyncInbox creates the user's inbox channel when they have none, and
// invites them to it, again for a channel that exists, so a retry after a
// failed invite still adds them.
func (d Deps) SyncInbox(ctx context.Context, userID string) (InboxChannelOutput, error) {
	var out InboxChannelOutput
	u, err := d.Store.UserByID(ctx, userID)
	if err != nil || u == nil || u.MemoryProject == "" || u.SlackUserID == "" {
		return out, err
	}
	c := store.Channel{Kind: store.KindInbox}
	if _, out.Created, err = d.openChannel(ctx, u, c, InboxChannelName(u.DisplayName), d.inboxIntro()); err != nil {
		return out, err
	}
	if out.Created != "" {
		slog.Info("inbox channel created", "user", u.ID, "channel", out.Created)
	}
	return out, nil
}

// inboxIntro introduces the inbox channel.
func (d Deps) inboxIntro() string {
	return fmt.Sprintf("This is your inbox channel. Every morning at %d:00 (%s) I'll post what needs your attention: "+
		"tasks that are due and events coming up, with what you need for each. On days with nothing due, there's no post.\n\n"+
		"Reply in a digest's thread to mark something done or move it, or tell me here about anything to keep track of, "+
		"no @mention needed.", InboxHour, d.Location)
}

// InboxChannelName is the Slack channel name for a user's inbox channel:
// inbox-<slug of their name>.
func InboxChannelName(userName string) string {
	return channelName("inbox", userName, 40)
}

// NewsChannelName is the Slack channel name for a user's news channel:
// news-shorts-<slug of their name>. Channel names are unique across the
// workspace, private ones included.
func NewsChannelName(userName string) string {
	return channelName("news-shorts", userName, 40)
}
