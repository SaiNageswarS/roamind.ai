package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/agent"
	"roamind.ai/agent/protocol"
	"roamind.ai/memory/store"
	"roamind.ai/memory/store/notes"
)

// MailMessageName is the mail-message workflow, which saves what one mail
// from a user's Gmail is about into their memory:
//
//	check → relate → save
//
// Each Gmail account's Apps Script (deploy/apps-script) gives its new mail
// a broad category and posts it to the gmail webhook, which Hatchet checks
// the signature of and turns into a MailEventKey event. check finds the
// account's user and records the message, so a message sent again is
// related once; relate has the agent find what the mail is about in
// memory and say which notes to write; save writes them, with the item
// rows of tasks and events, which the inbox digest shows when due.
const MailMessageName = "mail-message"

// MailEventKey is the event key of the gmail webhook
// (worker/cmd/setup-webhook).
const MailEventKey = "mail:received"

// MailStaleAfter is how long an account's script can send nothing, not
// even its daily heartbeat, before the inbox digest says so.
const MailStaleAfter = 48 * time.Hour

// MailInput is a request of a mail script: a message, or a heartbeat with
// only the address.
type MailInput struct {
	agent.MailMessage
	Heartbeat bool `json:"heartbeat,omitempty"`
}

// mailCheck is check's result; Skip is always present, so the later
// tasks' skip condition can read it.
type mailCheck struct {
	Skip   string `json:"skip"`
	UserID string `json:"user_id,omitempty"`
}

// mailRelate is relate's result.
type mailRelate struct {
	Related agent.RelatedMail `json:"related"`
	CostUSD float64           `json:"cost_usd,omitempty"`
}

// mailSave is save's result.
type mailSave struct {
	Notes []string `json:"notes,omitempty"` // permalinks written
	// Dropped are the items not saved, each with why.
	Dropped []string `json:"dropped,omitempty"`
}

// MailMessage declares the mail-message workflow. Runs are serialized
// across all accounts, so one user's notes are never written by two runs
// at once: the event names the account, not the user.
func MailMessage(c *hatchet.Client, d Deps) *hatchet.Workflow {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	wf := c.NewWorkflow(MailMessageName,
		hatchet.WithWorkflowEvents(MailEventKey),
		hatchet.WithWorkflowDescription("Save what one mail from a user's Gmail is about into their memory"),
		hatchet.WithWorkflowConcurrency(hatchet.Concurrency{
			Expression:    "'mail'",
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}),
	)
	check := wf.NewTask("check", func(ctx hatchet.Context, in MailInput) (mailCheck, error) {
		return d.checkMail(ctx, in)
	},
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	relate := wf.NewTask("relate", func(ctx hatchet.Context, in MailInput) (mailRelate, error) {
		var chk mailCheck
		if err := ctx.ParentOutput(check, &chk); err != nil {
			return mailRelate{}, err
		}
		return d.relateMail(ctx, in, chk)
	},
		hatchet.WithParents(check),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		// The turn only reads memory.
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(10*time.Minute),
	)
	wf.NewTask("save", func(ctx hatchet.Context, in MailInput) (mailSave, error) {
		var chk mailCheck
		var rel mailRelate
		if err := ctx.ParentOutput(check, &chk); err != nil {
			return mailSave{}, err
		}
		if err := ctx.ParentOutput(relate, &rel); err != nil {
			return mailSave{}, err
		}
		return d.saveMail(ctx, in, chk, rel)
	},
		hatchet.WithParents(check, relate),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		// Notes overwrite by title and rows upsert by key, so it can retry.
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(5*time.Minute),
	)
	wf.OnFailure(func(ctx hatchet.Context, in MailInput) (struct{}, error) {
		return struct{}{}, d.Store.FinishMessage(ctx, in.Address, in.MessageID, store.MailFailed)
	})
	return wf
}

// checkMail finds the account's user and records the request; a message
// is related when its user has memory and it wasn't related before.
func (d Deps) checkMail(ctx context.Context, in MailInput) (mailCheck, error) {
	var out mailCheck
	acct, err := d.Store.MailAccountOf(ctx, in.Address)
	if err != nil {
		return out, err
	}
	if acct == nil {
		// The signature was good, so the script is ours: it runs in an
		// account task mail-connect hasn't added.
		slog.Warn("mail from an account not connected", "address", in.Address)
		out.Skip = "the account " + in.Address + " is not connected"
		return out, nil
	}
	if err := d.Store.SeenMail(ctx, acct.Address, d.now()); err != nil {
		return out, err
	}
	switch {
	case in.Heartbeat:
		out.Skip = "heartbeat"
		return out, nil
	case strings.TrimSpace(in.MessageID) == "":
		out.Skip = "no message id"
		return out, nil
	}
	u, err := d.Store.UserByID(ctx, acct.UserID)
	if err != nil {
		return out, err
	}
	if u == nil || u.MemoryProject == "" || d.Memory == nil {
		out.Skip = "the user has no memory"
		return out, nil
	}
	ok, err := d.Store.PickMessage(ctx, store.MailMessage{Address: acct.Address, MessageID: in.MessageID,
		UserID: u.ID, Category: in.Category})
	if err != nil {
		return out, err
	}
	if !ok {
		out.Skip = "the message was sent before"
		return out, nil
	}
	out.UserID = u.ID
	return out, nil
}

// relateMail has the agent find what the mail is about in the user's
// memory and say which notes to write, as structured output. The mail is
// third-party text, so the turn can only read memory and search the web
// (ToolsRecall); the harness writes the notes.
func (d Deps) relateMail(ctx context.Context, in MailInput, chk mailCheck) (mailRelate, error) {
	var out mailRelate
	u, err := d.Store.UserByID(ctx, chk.UserID)
	if err != nil || u == nil {
		return out, fmt.Errorf("user %s: %v", chk.UserID, err)
	}
	goals, err := d.Memory.Goals(u.MemoryProject)
	if err != nil {
		return out, err
	}
	r := agent.MailRelating{UserName: u.DisplayName, UserEmail: u.Email, Message: in.MailMessage,
		Date: d.now().In(d.Location).Format(time.DateOnly), Timezone: d.Location.String()}
	for _, g := range goals {
		if g.Active() {
			r.Goals = append(r.Goals, agent.MailGoal{Title: g.Title, Permalink: g.Permalink})
		}
	}
	prompt, err := agent.RelateMailPrompt(r)
	if err != nil {
		return out, err
	}
	turn, err := agent.RunTurn(ctx, d.Agent, protocol.TurnInput{
		UserID:        u.ID,
		Text:          prompt,
		UserName:      u.DisplayName,
		UserEmail:     u.Email,
		MemoryProject: u.MemoryProject,
		Tools:         protocol.ToolsRecall,
		OutputSchema:  agent.RelatedMailSchema,
	}, func(protocol.Packet) {})
	out.CostUSD = turn.CostUSD
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(turn.Structured, &out.Related); err != nil {
		return out, fmt.Errorf("related mail: %w", err)
	}
	return out, nil
}

// saveMail writes the notes the turn returned and their item rows, and
// records the message as done, or skipped when there was nothing to save.
// An item that doesn't fit its schema's layout is dropped, with why, so
// the others are still saved.
func (d Deps) saveMail(ctx context.Context, in MailInput, chk mailCheck, rel mailRelate) (mailSave, error) {
	var out mailSave
	u, err := d.Store.UserByID(ctx, chk.UserID)
	if err != nil || u == nil {
		return out, fmt.Errorf("user %s: %v", chk.UserID, err)
	}
	for _, it := range rel.Related.Items {
		permalink, why, err := d.saveRelated(ctx, u, in, it)
		if err != nil {
			return out, err
		}
		if why != "" {
			slog.Warn("mail item dropped", "user", u.ID, "message", in.MessageID, "title", it.Title, "why", why)
			out.Dropped = append(out.Dropped, it.Title+": "+why)
			continue
		}
		out.Notes = append(out.Notes, permalink)
	}
	status := store.MailDone
	if len(out.Notes) == 0 {
		status = store.MailSkipped
	}
	if err := d.Store.FinishMessage(ctx, in.Address, in.MessageID, status); err != nil {
		return out, err
	}
	slog.Info("mail saved", "user", u.ID, "message", in.MessageID, "notes", len(out.Notes), "skip", rel.Related.Skip)
	return out, nil
}

var (
	// mailFolders are the folders a new note of each kind may go in, as
	// the schemas say.
	mailFolders = map[string]*regexp.Regexp{
		store.ItemTask:  regexp.MustCompile(`^tasks(/[a-z0-9][a-z0-9-]*)?$`),
		store.ItemEvent: regexp.MustCompile(`^events/[0-9]{4}$`),
		"person":        regexp.MustCompile(`^people$`),
	}
	itemStates = []string{store.ItemOpen, store.ItemDone, store.ItemCancelled}
)

// yamlScalar is s as a YAML scalar: quoted when it holds a colon or a
// hash, or starts with a character YAML reads as syntax.
func yamlScalar(s string) string {
	if strings.ContainsAny(s, ":#") || strings.ContainsAny(s[:1], `'"[]{}>|*&!%@,-?`+"`") {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return s
}

// saveRelated writes one item's note, and its row for a task or event,
// and returns the note's permalink; why says why the item was dropped
// instead. An update rewrites the note's file in place.
func (d Deps) saveRelated(ctx context.Context, u *store.User, in MailInput, it agent.RelatedItem) (permalink, why string, err error) {
	project := u.MemoryProject
	if _, ok := mailFolders[it.Kind]; !ok {
		return "", "kind " + it.Kind + " is not task, event or person", nil
	}
	content := strings.TrimSpace(it.Content)
	if content == "" {
		return "", "no content", nil
	}
	var folder, title string
	found, err := d.Memory.FindNote(ctx, project, strings.TrimSpace(it.Note))
	if err != nil {
		return "", "", err
	}
	switch {
	case found != nil:
		if found.Type != it.Kind {
			return "", "the note " + it.Note + " has type " + found.Type + ", not " + it.Kind, nil
		}
		permalink, folder, title = found.Permalink, found.Folder, found.Name
	default:
		folder, title = strings.Trim(strings.TrimSpace(it.Folder), "/"), strings.TrimSpace(it.Title)
		if !mailFolders[it.Kind].MatchString(folder) {
			return "", "folder " + it.Folder + " is not where " + it.Kind + " notes go", nil
		}
		if title == "" || strings.ContainsAny(title, "/\\\n") || len(title) > 200 {
			return "", "the title is empty, too long, or has a slash or line break", nil
		}
		if it.Kind == "person" {
			has, err := d.Memory.HasContact(project, it.Contact)
			if err != nil {
				return "", "", err
			}
			if has {
				return "", "a person note in people/ already lists " + it.Contact, nil
			}
		}
		permalink = notes.Permalink(project, folder, title)
	}

	var row *store.Item
	if it.Kind != "person" {
		row = &store.Item{UserID: u.ID, Key: strings.TrimSpace(it.Key), Kind: it.Kind, Note: permalink, Title: title,
			Date: strings.TrimSpace(it.Date), State: it.State, Source: "mail:" + in.Address + "/" + in.MessageID}
		if row.Key == "" {
			row.Key = title
		}
		if it.Kind == store.ItemEvent {
			row.EventKind = it.EventKind
			if row.EventKind != "" && !slices.Contains(store.EventKinds, row.EventKind) {
				return "", "event kind " + it.EventKind + " is not the Event schema's", nil
			}
		}
		if row.State != "" && !slices.Contains(itemStates, row.State) {
			return "", "state " + it.State + " is not open, done or cancelled", nil
		}
		if row.ShowFrom, row.Until, err = store.InboxLeads(row.Kind, row.EventKind, row.Date,
			strings.TrimSpace(it.RemindOn), strings.TrimSpace(it.End)); err != nil {
			return "", err.Error(), nil
		}
	}

	note := fmt.Sprintf("---\ntitle: %s\ntype: %s\npermalink: %s\n---\n\n%s\n", yamlScalar(title), it.Kind, permalink, content)
	if err := d.Memory.WriteNote(ctx, project, folder, title, it.Kind, note); err != nil {
		return "", "", err
	}
	if row != nil {
		if err := d.Store.PutItem(ctx, *row); err != nil {
			return "", "", err
		}
	}
	return permalink, "", nil
}
