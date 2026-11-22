package workflows

import (
	"context"
	"log/slog"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/agent/protocol"
	"roamind.ai/memory/store"
)

// FinanceChannelName gives a user with accounts their finance channel. The
// ledger itself needs no workflow: every import settles it (store
// ImportFinanceReport).
const FinanceChannelName = "finance-channel"

// FinanceUserInput names a user.
type FinanceUserInput struct {
	UserID string `json:"user_id"`
}

// FinanceChannelOutput names the channel a run created.
type FinanceChannelOutput struct {
	Created string `json:"created,omitempty"` // channel name
}

func perUser() hatchet.TaskOption {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	return hatchet.WithConcurrency(&hatchet.Concurrency{Expression: "input.user_id", MaxRuns: &maxRuns, LimitStrategy: &strategy})
}

// FinanceChannel declares the finance-channel task.
func FinanceChannel(c *hatchet.Client, d Deps) *hatchet.StandaloneTask {
	return c.NewStandaloneTask(FinanceChannelName, func(ctx hatchet.Context, in FinanceUserInput) (FinanceChannelOutput, error) {
		return d.financeChannel(ctx, in.UserID)
	},
		hatchet.WithWorkflowDescription("Create a user's finance channel"),
		perUser(),
		hatchet.WithRetries(3),
		hatchet.WithExecutionTimeout(time.Minute),
	)
}

// financeChannel opens the user's finance channel once they have an
// account.
func (d Deps) financeChannel(ctx context.Context, userID string) (FinanceChannelOutput, error) {
	var out FinanceChannelOutput
	u, err := d.Store.UserByID(ctx, userID)
	if err != nil || u == nil || u.SlackUserID == "" {
		return out, err
	}
	as, err := d.Store.FinAccounts(ctx, userID)
	if err != nil || len(as) == 0 {
		return out, err
	}
	intro := "This channel is for your investments. Upload your broker reports here (tradebooks, statements, " +
		"contract notes) and I'll import their trades into your ledger, with each sell matched to the buys it " +
		"came from, first in, first out. Ask me about an import here, no @mention needed."
	_, out.Created, err = d.openChannel(ctx, u, store.Channel{Kind: store.KindFinance}, FinanceChannelSlackName(u.DisplayName), intro)
	if out.Created != "" {
		slog.Info("finance channel created", "user", u.ID, "channel", out.Created)
	}
	return out, err
}

// FinanceChannelSlackName is the Slack name of a user's finance channel.
func FinanceChannelSlackName(userName string) string {
	return channelName("finance", userName, 40)
}

// financeTrigger returns a hook for chat-turn: after a turn it opens the
// finance channel of a user who has an account but no channel.
func financeTrigger(channel *hatchet.StandaloneTask, d Deps) func(ctx context.Context, u *store.User, _ FinishedTurn) error {
	return func(ctx context.Context, u *store.User, _ FinishedTurn) error {
		ch, err := d.Store.Channel(ctx, u.ID, store.KindFinance, "")
		if err != nil || ch != "" || u.SlackUserID == "" {
			return err
		}
		as, err := d.Store.FinAccounts(ctx, u.ID)
		if err == nil && len(as) > 0 {
			_, err = channel.RunNoWait(ctx, FinanceUserInput{UserID: u.ID})
		}
		return err
	}
}

// financeRef is the context of a turn in the user's finance channel.
func (d Deps) financeRef(ctx context.Context, userID string) (*protocol.FinanceRef, error) {
	ref := &protocol.FinanceRef{}
	as, err := d.Store.FinAccounts(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		ref.Accounts = append(ref.Accounts, a.ID+" "+a.Label())
	}
	fs, err := d.Store.FinanceSenders(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		if !f.Trusted && f.SeenAt.After(d.now().AddDate(0, 0, -30)) {
			ref.Untrusted = append(ref.Untrusted, f.Domain)
		}
	}
	return ref, nil
}
