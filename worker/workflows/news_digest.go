package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/agent/protocol"
	"roamind.ai/channels/feeds"
	"roamind.ai/memory/store"
)

// NewsDigestName is the news-digest workflow, which posts one digest in a
// user's news channel:
//
//	check-skip → gather → rank → post
//
// gather fetches the feeds of the user's interests and keeps the new
// items not sent to them before; rank has the agent judge each interest's
// items by that interest's rubric, one call per interest; post keeps the
// best ones, one per story, sends them and records their links and stories
// as sent. Feedback on a digest (reactions and thread replies) is recorded
// by news-reaction and chat-turn, and weighs its interests in later
// digests.
const NewsDigestName = "news-digest"

const (
	// feedbackWindow is how far back feedback weighs an interest.
	feedbackWindow = 30 * 24 * time.Hour
	// freshWindow is how old a feed item may be to make a digest; slow,
	// evergreen feeds such as Stoic writing post a few times a month.
	freshWindow = 30 * 24 * time.Hour
	// perFeed and perInterest cap the candidates: the newest items of each
	// feed, then of each interest. The rubric does the filtering, so many
	// reach it.
	perFeed     = 3
	perInterest = 25
	// rankers is how many interests are ranked at once.
	rankers = 3
	// minScore is the combined score, the mean of an item's rubric scores
	// (0-10), it needs to be sent.
	minScore = 5.0
	// storyWindow is how long a story sent stays out of digests; later
	// developments in it may come back after that.
	storyWindow = 48 * time.Hour
	// maxItems, maxPerInterest and maxPerCategory cap the items of a digest;
	// categories are those of an interest's rubric.
	maxItems       = 4
	maxPerInterest = 2
	maxPerCategory = 1
)

// DigestInput names the user and slot of a digest.
type DigestInput struct {
	UserID string `json:"user_id"`
	Slot   string `json:"slot"` // local "YYYY-MM-DD HH"
}

// digestCheck is check-skip's result; Skip is always present, so the later
// tasks' skip condition can read it.
type digestCheck struct {
	Skip      string `json:"skip"`
	ChannelID string `json:"channel_id,omitempty"`
}

// digestGather is gather's result: the candidates and the rubrics of their
// interests.
type digestGather struct {
	Interests  []rankInterest          `json:"interests,omitempty"`
	Candidates []agent.DigestCandidate `json:"candidates,omitempty"`
	// FailedFeeds could not be read this time.
	FailedFeeds []string `json:"failed_feeds,omitempty"`
}

// rankInterest is an interest with candidates, and its rubric.
type rankInterest struct {
	Name   string `json:"name"`
	Rubric string `json:"rubric"`
}

// digestRank is rank's result: the judgements of every interest ranked.
type digestRank struct {
	Judgements []agent.Judgement `json:"judgements,omitempty"`
	// Failed are the interests whose ranking failed this time.
	Failed  []string `json:"failed,omitempty"`
	CostUSD float64  `json:"cost_usd,omitempty"`
}

// digestPost is post's result.
type digestPost struct {
	Skipped  string `json:"skipped,omitempty"`
	TS       string `json:"ts,omitempty"`
	DigestID int64  `json:"digest_id,omitempty"`
	Items    int    `json:"items,omitempty"`
}

// NewsDigest declares the news-digest workflow. Runs are serialized per
// user. As in learning-exercise, every task after check-skip lists it as a
// parent, so each one is skipped when check-skip gives a reason.
func NewsDigest(c *hatchet.Client, d Deps) *hatchet.Workflow {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	wf := c.NewWorkflow(NewsDigestName,
		hatchet.WithWorkflowDescription("Post a news-shorts digest for one user"),
		hatchet.WithWorkflowConcurrency(hatchet.Concurrency{
			Expression:    "input.user_id",
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}),
	)
	check := wf.NewTask("check-skip", func(ctx hatchet.Context, in DigestInput) (digestCheck, error) {
		return d.checkDigest(ctx, in)
	},
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	gather := wf.NewTask("gather", func(ctx hatchet.Context, in DigestInput) (digestGather, error) {
		return d.gatherDigest(ctx, in)
	},
		hatchet.WithParents(check),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(2*time.Minute),
	)
	rank := wf.NewTask("rank", func(ctx hatchet.Context, in DigestInput) (digestRank, error) {
		var g digestGather
		if err := ctx.ParentOutput(gather, &g); err != nil {
			return digestRank{}, err
		}
		return d.rankDigest(ctx, in, g)
	},
		hatchet.WithParents(check, gather),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		// Ranking has no side effects besides the claude sessions.
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(15*time.Minute),
	)
	wf.NewTask("post", func(ctx hatchet.Context, in DigestInput) (digestPost, error) {
		var chk digestCheck
		var g digestGather
		var r digestRank
		if err := ctx.ParentOutput(check, &chk); err != nil {
			return digestPost{}, err
		}
		if err := ctx.ParentOutput(gather, &g); err != nil {
			return digestPost{}, err
		}
		if err := ctx.ParentOutput(rank, &r); err != nil {
			return digestPost{}, err
		}
		return d.postDigest(ctx, in, chk, g, r)
	},
		hatchet.WithParents(check, gather, rank),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		// A retry could post the digest twice.
		hatchet.WithRetries(0),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	return wf
}

// checkDigest decides whether the user gets this slot's digest: they need
// a news channel and active interests, and the slot must not be posted yet.
func (d Deps) checkDigest(ctx context.Context, in DigestInput) (digestCheck, error) {
	var out digestCheck
	u, err := d.Store.UserByID(ctx, in.UserID)
	if err != nil {
		return out, err
	}
	if u == nil {
		out.Skip = "no such user"
		return out, nil
	}
	ch, err := d.Store.NewsChannelOf(ctx, u.ID)
	if err != nil {
		return out, err
	}
	if ch == "" {
		out.Skip = "the user has no news channel"
		return out, nil
	}
	posted, err := d.Store.DigestPosted(ctx, u.ID, in.Slot)
	if err != nil {
		return out, err
	}
	if posted {
		out.Skip = "the digest of " + in.Slot + " is already posted"
		return out, nil
	}
	interests, err := d.Store.ActiveInterests(ctx, u.ID)
	if err != nil {
		return out, err
	}
	if len(interests) == 0 {
		out.Skip = "no active interests"
		return out, nil
	}
	out.ChannelID = ch
	return out, nil
}

// gatherDigest fetches the feeds of the user's active interests and returns
// the fresh items not sent to the user before, each once: the newest
// perFeed of each feed, then the newest perInterest of each interest.
func (d Deps) gatherDigest(ctx context.Context, in DigestInput) (digestGather, error) {
	var out digestGather
	interests, err := d.Store.ActiveInterests(ctx, in.UserID)
	if err != nil {
		return out, err
	}
	fetched, failed := d.fetchFeeds(ctx, interests)
	out.FailedFeeds = failed
	if len(failed) > 0 && len(fetched) == 0 {
		return out, fmt.Errorf("no feed could be read: %s", strings.Join(failed, ", "))
	}

	cutoff := d.now().Add(-freshWindow)
	seen := map[string]bool{} // URL hashes taken by an earlier interest
	for _, it := range interests {
		var entries []feeds.Entry
		for _, src := range it.Sources {
			n := 0
			for _, e := range fetched[src] {
				if n == perFeed {
					break
				}
				if !e.Published.IsZero() && e.Published.Before(cutoff) {
					continue
				}
				if h := store.URLHash(e.URL); !seen[h] {
					seen[h] = true
					entries = append(entries, e)
					n++
				}
			}
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Published.After(entries[j].Published) })
		urls := make([]string, len(entries))
		for i, e := range entries {
			urls[i] = e.URL
		}
		unsent, err := d.Store.Unsent(ctx, in.UserID, urls)
		if err != nil {
			return out, err
		}
		keep := map[string]bool{}
		for _, u := range unsent {
			keep[u] = true
		}
		n := 0
		for _, e := range entries {
			if !keep[e.URL] || n == perInterest {
				continue
			}
			n++
			c := agent.DigestCandidate{
				ID: "c" + strconv.Itoa(len(out.Candidates)+1), Interest: it.Name,
				Title: e.Title, URL: e.URL, Source: e.Feed, Content: e.Content,
			}
			if !e.Published.IsZero() {
				c.Published = e.Published.In(d.Location).Format(time.DateOnly)
			}
			out.Candidates = append(out.Candidates, c)
		}
		if n > 0 {
			out.Interests = append(out.Interests, rankInterest{Name: it.Name, Rubric: it.Rubric})
		}
	}
	return out, nil
}

// fetchFeeds reads every feed of the interests once, in parallel, and
// returns the entries by feed URL and the feeds that failed.
func (d Deps) fetchFeeds(ctx context.Context, interests []store.Interest) (map[string][]feeds.Entry, []string) {
	var urls []string
	seen := map[string]bool{}
	for _, it := range interests {
		for _, src := range it.Sources {
			if !seen[src] {
				seen[src] = true
				urls = append(urls, src)
			}
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	fetched := map[string][]feeds.Entry{}
	var failed []string
	for _, u := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			es, err := d.Feeds.Fetch(ctx, u)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				slog.Warn("feed not read", "feed", u, "err", err)
				failed = append(failed, u)
				return
			}
			fetched[u] = es
		}()
	}
	wg.Wait()
	sort.Strings(failed)
	return fetched, failed
}

// rankDigest has the agent judge each interest's candidates by its
// rubric, one structured-output turn per interest, rankers at a time. An
// interest whose turn fails is left out of this digest; the task fails
// only when every one does.
func (d Deps) rankDigest(ctx context.Context, in DigestInput, g digestGather) (digestRank, error) {
	var out digestRank
	if len(g.Candidates) == 0 {
		return out, nil
	}
	u, err := d.Store.UserByID(ctx, in.UserID)
	if err != nil || u == nil {
		return out, fmt.Errorf("user %s: %v", in.UserID, err)
	}
	slot, err := time.ParseInLocation("2006-01-02 15", in.Slot, d.Location)
	if err != nil {
		return out, fmt.Errorf("slot %q: %w", in.Slot, err)
	}
	// The stories sent lately, so the same event keeps its slug and is
	// recognised as sent.
	sent, err := d.Store.SentStoriesSince(ctx, in.UserID, d.now().Add(-storyWindow))
	if err != nil {
		return out, err
	}
	recent := make([]agent.RecentStory, len(sent))
	for i, st := range sent {
		recent[i] = agent.RecentStory{Story: st.Story, Title: st.Title}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, rankers)
	var errs []error
	for _, it := range g.Interests {
		var cands []agent.DigestCandidate
		for _, c := range g.Candidates {
			if c.Interest == it.Name {
				cands = append(cands, c)
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			js, cost, err := d.rankInterest(ctx, u, agent.DigestRanking{
				UserName: u.DisplayName, Date: slot.Format(time.DateOnly), Time: slot.Format("15:04"),
				Interest: it.Name, Rubric: it.Rubric, Candidates: cands, RecentStories: recent,
			})
			mu.Lock()
			defer mu.Unlock()
			out.CostUSD += cost
			if err != nil {
				slog.Warn("interest not ranked", "user", u.ID, "interest", it.Name, "err", err)
				out.Failed = append(out.Failed, it.Name)
				errs = append(errs, fmt.Errorf("%s: %w", it.Name, err))
				return
			}
			out.Judgements = append(out.Judgements, js...)
		}()
	}
	wg.Wait()
	sort.Strings(out.Failed)
	if len(out.Failed) == len(g.Interests) {
		return out, errors.Join(errs...)
	}
	return out, nil
}

// rankInterest runs one interest's ranking turn and returns its judgements
// of the given candidates only.
func (d Deps) rankInterest(ctx context.Context, u *store.User, r agent.DigestRanking) ([]agent.Judgement, float64, error) {
	prompt, err := agent.RankDigestPrompt(r)
	if err != nil {
		return nil, 0, err
	}
	turn, err := agent.RunTurn(ctx, d.Agent, protocol.TurnInput{
		UserID:    u.ID,
		Text:      prompt,
		UserName:  u.DisplayName,
		UserEmail: u.Email,
		// The candidates are third-party text: the turn gets no tools.
		Tools:        protocol.ToolsNone,
		OutputSchema: agent.RankDigestSchema,
	}, func(protocol.Packet) {})
	if err != nil {
		return nil, turn.CostUSD, err
	}
	var ranked agent.RankedInterest
	if err := json.Unmarshal(turn.Structured, &ranked); err != nil {
		return nil, turn.CostUSD, fmt.Errorf("ranking: %w", err)
	}
	ids := map[string]bool{}
	for _, c := range r.Candidates {
		ids[c.ID] = true
	}
	var js []agent.Judgement
	for _, j := range ranked.Judgements {
		if ids[j.ID] {
			js = append(js, j)
		}
	}
	return js, turn.CostUSD, nil
}

// digestItem is a candidate picked for the digest.
type digestItem struct {
	agent.DigestCandidate
	Judgement agent.Judgement
	Score     float64
}

// postDigest picks the best candidates, reserves the slot, posts them as
// one message of text and links, without link previews, and records the
// digest, its links and its stories as sent. A reply in its thread starts a new session that is
// given the digest's text.
func (d Deps) postDigest(ctx context.Context, in DigestInput, chk digestCheck, g digestGather, r digestRank) (digestPost, error) {
	var out digestPost
	now := d.now()
	signals, err := d.Store.InterestSignals(ctx, in.UserID, now.Add(-feedbackWindow))
	if err != nil {
		return out, err
	}
	sent, err := d.Store.SentStoriesSince(ctx, in.UserID, now.Add(-storyWindow))
	if err != nil {
		return out, err
	}
	sentStories := map[string]bool{}
	for _, st := range sent {
		sentStories[st.Story] = true
	}
	items := pickItems(g, r, signals, sentStories)
	if len(items) == 0 {
		out.Skipped = fmt.Sprintf("none of %d new items was included with a score of %.0f or more", len(g.Candidates), minScore)
		return out, nil
	}
	slot, err := time.ParseInLocation("2006-01-02 15", in.Slot, d.Location)
	if err != nil {
		return out, fmt.Errorf("slot %q: %w", in.Slot, err)
	}
	header := "News shorts · " + slot.Format("15:04")
	// The slot is reserved before posting, so a run that dies after posting
	// is never replayed into a second post.
	id, ok, err := d.Store.ReserveDigest(ctx, in.UserID, chk.ChannelID, in.Slot)
	if err != nil {
		return out, err
	}
	if !ok {
		out.Skipped = "the digest of " + in.Slot + " is already posted"
		return out, nil
	}
	_, ts, err := d.Slack.PostMessageContext(ctx, chk.ChannelID,
		slackapi.MsgOptionText(digestMessage(header, items), false),
		slackapi.MsgOptionDisableLinkUnfurl())
	if err != nil {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if rerr := d.Store.ReleaseDigest(releaseCtx, id); rerr != nil {
			slog.Error("release digest slot", "user", in.UserID, "slot", in.Slot, "err", rerr)
		}
		return out, fmt.Errorf("post digest: %w", err)
	}
	out.TS, out.Items, out.DigestID = ts, len(items), id
	saved := make([]store.DigestItem, len(items))
	for i, it := range items {
		saved[i] = store.DigestItem{Interest: it.Interest, Title: it.Title, URL: it.URL, Story: it.Judgement.Story}
	}
	// The message, its items, and their links and stories as sent, at once.
	if err := d.Store.CompleteDigest(ctx, store.Digest{
		ID: id, UserID: in.UserID, TS: ts, Text: digestText(header, items), PostedAt: now,
	}, saved); err != nil {
		return out, err
	}
	if err := d.Store.SaveThread(ctx, store.Thread{ChannelID: chk.ChannelID, ThreadTS: ts, UserID: in.UserID}); err != nil {
		return out, err
	}
	slog.Info("digest posted", "user", in.UserID, "slot", in.Slot, "items", len(items), "candidates", len(g.Candidates))
	return out, nil
}

// pickItems keeps the candidates the rubric includes with a combined score,
// the mean of their scores, of at least minScore, whose story was not sent
// in the last storyWindow. It returns the best maxItems of them: one per
// story, at most maxPerInterest per interest and maxPerCategory per
// category of an interest. Feedback moves an interest's items up or down by
// up to a point: half a point per net like, capped at two.
func pickItems(g digestGather, r digestRank, signals []store.InterestSignal, sentStories map[string]bool) []digestItem {
	byID := map[string]agent.DigestCandidate{}
	for _, c := range g.Candidates {
		byID[c.ID] = c
	}
	bonus := map[string]float64{}
	for _, s := range signals {
		bonus[s.Interest] = 0.5 * float64(max(-2, min(2, s.Liked-s.Disliked)))
	}
	var scored []digestItem
	for _, j := range r.Judgements {
		c, ok := byID[j.ID]
		if !ok || !j.Include || j.Mean() < minScore || strings.TrimSpace(j.Summary) == "" || sentStories[j.Story] {
			continue
		}
		delete(byID, j.ID) // each candidate once
		scored = append(scored, digestItem{DigestCandidate: c, Judgement: j, Score: j.Mean() + bonus[c.Interest]})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	var out []digestItem
	perInterest, perCategory, stories := map[string]int{}, map[string]int{}, map[string]bool{}
	for _, it := range scored {
		if len(out) == maxItems {
			break
		}
		story, category := it.Judgement.Story, it.Interest+"/"+it.Judgement.Category
		switch {
		case story != "" && stories[story]:
			continue
		case perInterest[it.Interest] == maxPerInterest:
			continue
		case it.Judgement.Category != "" && perCategory[category] == maxPerCategory:
			continue
		}
		stories[story] = true
		perInterest[it.Interest]++
		perCategory[category]++
		out = append(out, it)
	}
	return out
}

var (
	mrkdwnEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	linkEscaper   = strings.NewReplacer("|", "%7C", "<", "%3C", ">", "%3E")
)

// digestMessage is the digest as posted: the header, then per item its
// title linked to the source, its summary and its interest.
func digestMessage(header string, items []digestItem) string {
	var b strings.Builder
	b.WriteString("*" + mrkdwnEscaper.Replace(header) + "*")
	for _, it := range items {
		fmt.Fprintf(&b, "\n\n*<%s|%s>*\n%s\n_%s_", linkEscaper.Replace(it.URL), mrkdwnEscaper.Replace(it.Title),
			mrkdwnEscaper.Replace(it.Judgement.Summary), mrkdwnEscaper.Replace(it.Interest))
	}
	return b.String()
}

// digestText is the digest as the agent sees it in the digest's thread.
func digestText(header string, items []digestItem) string {
	var b strings.Builder
	b.WriteString(header + "\n")
	for i, it := range items {
		fmt.Fprintf(&b, "\n%d. %s (%s)\n%s\nInterest: %s\n", i+1, it.Title, it.URL, it.Judgement.Summary, it.Interest)
	}
	return b.String()
}
