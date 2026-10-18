package workflows

import (
	"context"
	"fmt"
	"slices"
	"strings"

	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/channels/slack"
	"roamind.ai/channels/web"
)

// SlackThread is the Slack thread a job request was made in.
type SlackThread struct {
	Channel  string `json:"channel"`
	ThreadTS string `json:"thread_ts"`
}

// jobReply is where a job request's results go: back to the channel it
// came from. A request from the web app gets them in the user's profile
// there (webReply), as its links' states and applications to review; one
// from Slack gets them in its thread (slackReply). A user may have either
// channel or both.
type jobReply interface {
	// done says the links' posting has an application already, appID.
	done(ctx context.Context, ids []string, appID, reason string) error
	// fail says why links couldn't be drafted, by link id.
	fail(ctx context.Context, reasons map[string]string) error
	// failPending is fail, with one reason, for those of the links not
	// done or failed yet.
	failPending(ctx context.Context, ids []string, reason string) error
	// publish delivers an application.
	publish(ctx context.Context, in JobApplicationInput, a agent.TailoredApplication) error
}

// jobReply is the reply to a request from the web app profile uid, or from
// the Slack thread th.
func (d Deps) jobReply(uid string, th *SlackThread) jobReply {
	if th != nil {
		return slackReply{d: d, thread: *th}
	}
	return webReply{d: d, uid: uid}
}

// webReply puts results in the web app profile uid.
type webReply struct {
	d   Deps
	uid string
}

func (r webReply) done(ctx context.Context, ids []string, appID, reason string) error {
	for _, id := range ids {
		if _, err := r.d.Web.SetLink(ctx, r.uid, id, web.LinkState{Status: web.LinkDone, Reason: reason, ApplicationID: appID}); err != nil {
			return err
		}
	}
	return nil
}

func (r webReply) fail(ctx context.Context, reasons map[string]string) error {
	for id, reason := range reasons {
		if _, err := r.d.Web.SetLink(ctx, r.uid, id, web.LinkState{Status: web.LinkFailed, Reason: reason}); err != nil {
			return err
		}
	}
	return nil
}

// failPending fails the links still processing; links done meanwhile, or
// removed, are left alone.
func (r webReply) failPending(ctx context.Context, ids []string, reason string) error {
	failed := map[string]string{}
	for _, id := range ids {
		s, err := r.d.Web.LinkStatus(ctx, r.uid, id)
		if err != nil {
			return err
		}
		if s == web.LinkProcessing {
			failed[id] = reason
		}
	}
	return r.fail(ctx, failed)
}

// publish saves the application under the posting's url hash and points
// the posting's links at it.
func (r webReply) publish(ctx context.Context, in JobApplicationInput, t agent.TailoredApplication) error {
	a := web.Application{
		Title: in.Posting.Title, Company: in.Posting.Company, Location: in.Posting.Location,
		URL: in.Posting.URL, ApplyURL: orDefault(in.Posting.ApplyURL, in.Posting.URL), Source: in.Posting.Source,
		PostedAt: in.Posting.Posted, Fit: web.Fit{Score: in.Score, Reasons: in.Reasons},
		CoverLetter: t.CoverLetter,
		Status:      web.StatusReady, CreatedAt: r.d.now(),
	}
	if len(in.LinkIDs) > 0 {
		a.LinkID = in.LinkIDs[0]
	}
	for _, ans := range t.Answers {
		a.Answers = append(a.Answers, web.Answer{Question: ans.Question, Required: ans.Required, Answer: ans.Answer})
	}
	if err := r.d.Web.SaveApplication(ctx, r.uid, in.URLHash, a); err != nil {
		return err
	}
	return r.done(ctx, in.LinkIDs, in.URLHash, "")
}

// slackReply posts results in a Slack thread. Its links' ids are their
// URLs, and it keeps no state: each result is a message.
type slackReply struct {
	d      Deps
	thread SlackThread
}

func (r slackReply) done(ctx context.Context, ids []string, _, reason string) error {
	reasons := map[string]string{}
	for _, id := range ids {
		reasons[id] = reason
	}
	return r.post(ctx, "Already done:\n"+linkList(reasons))
}

func (r slackReply) fail(ctx context.Context, reasons map[string]string) error {
	if len(reasons) == 0 {
		return nil
	}
	return r.post(ctx, "I couldn't draft applications for these:\n"+linkList(reasons))
}

func (r slackReply) failPending(ctx context.Context, ids []string, reason string) error {
	reasons := map[string]string{}
	for _, id := range ids {
		reasons[id] = reason
	}
	return r.fail(ctx, reasons)
}

// publish posts the application, in Markdown, for the user to copy.
func (r slackReply) publish(ctx context.Context, in JobApplicationInput, t agent.TailoredApplication) error {
	p := in.Posting
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**", orDefault(p.Title, "Your application"))
	if p.Company != "" {
		fmt.Fprintf(&b, ", %s", p.Company)
	}
	if p.Location != "" {
		fmt.Fprintf(&b, " (%s)", p.Location)
	}
	fmt.Fprintf(&b, "\n[Apply](%s) · fit %d/10", orDefault(p.ApplyURL, p.URL), in.Score)
	if len(in.Reasons) > 0 {
		b.WriteString(": " + strings.Join(in.Reasons, "; "))
	}
	b.WriteString("\n\n**Cover letter**\n\n" + strings.TrimSpace(t.CoverLetter))
	if len(t.Answers) > 0 {
		b.WriteString("\n\n**Form answers**")
		for _, a := range t.Answers {
			fmt.Fprintf(&b, "\n\n**%s**", a.Question)
			if a.Required {
				b.WriteString(" (required)")
			}
			b.WriteString("\n" + strings.TrimSpace(a.Answer))
		}
	}
	return r.post(ctx, b.String())
}

// post posts Markdown in the thread, in as many messages as it takes.
func (r slackReply) post(ctx context.Context, md string) error {
	for _, c := range slack.Split(slack.ToMrkdwn(md), slack.MaxChunk) {
		if _, _, err := r.d.Slack.PostMessageContext(ctx, r.thread.Channel,
			slackapi.MsgOptionText(c, false), slackapi.MsgOptionTS(r.thread.ThreadTS)); err != nil {
			return fmt.Errorf("post in %s: %w", r.thread.Channel, err)
		}
	}
	return nil
}

// linkList lists links with their reasons, in URL order.
func linkList(reasons map[string]string) string {
	urls := make([]string, 0, len(reasons))
	for u := range reasons {
		urls = append(urls, u)
	}
	slices.Sort(urls)
	var b strings.Builder
	for _, u := range urls {
		fmt.Fprintf(&b, "- %s: %s\n", u, reasons[u])
	}
	return strings.TrimRight(b.String(), "\n")
}
