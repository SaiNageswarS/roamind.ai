package workflows

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/agent/protocol"
	"roamind.ai/memory/finance"
	"roamind.ai/memory/store"
)

// Broker mail. A mail from a domain the user trusts (finance_senders),
// that Gmail found signed by it, is read into their ledger instead of
// their memory: check routes it, relate reads its trades with a small
// model (extractTrades), and save checks and writes them (saveTrades).
// Finance mail from any other domain only lists the domain, untrusted,
// for the user to trust or not: anyone can send mail that looks like a
// trade confirmation.

// mailWindow is how long before the mail's date its trades may be dated:
// a confirmation comes within days, a statement covers weeks.
var mailWindow = map[string]time.Duration{
	finance.DocConfirmation: 10 * 24 * time.Hour,
	finance.DocContractNote: 10 * 24 * time.Hour,
	finance.DocStatement:    45 * 24 * time.Hour,
}

// financeMail decides whether the mail goes to the user's ledger: trusted
// is its trusted domain when it does; skip, when it goes nowhere.
func (d Deps) financeMail(ctx context.Context, userID string, in MailInput) (trusted, skip string, err error) {
	domain := senderDomain(in.From)
	if domain == "" {
		return "", "", nil
	}
	if trusted, err = d.Store.TrustedFinanceDomain(ctx, userID, domain); err != nil {
		return "", "", err
	}
	if trusted == "" {
		if in.Category != "finance" {
			return "", "", nil // not broker mail: the memory's
		}
		if err := d.Store.SeenFinanceSender(ctx, userID, domain, in.Subject, d.now()); err != nil {
			return "", "", err
		}
		return "", "finance mail from " + domain + ", which the user doesn't trust", nil
	}
	if err := d.Store.SeenFinanceSender(ctx, userID, trusted, in.Subject, d.now()); err != nil {
		return "", "", err
	}
	if !signedBy(in.AuthResults, trusted) {
		slog.Warn("broker mail not signed by its domain", "user", userID, "domain", domain, "message", in.MessageID)
		return "", "mail from " + domain + " that " + trusted + " didn't sign", nil
	}
	return trusted, "", nil
}

// senderDomain is the lowercase domain of a From header, "" when it has
// none.
func senderDomain(from string) string {
	a, err := mail.ParseAddress(from)
	if err != nil {
		return ""
	}
	_, domain, ok := strings.Cut(a.Address, "@")
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(domain, "."))
}

// signedBy reports whether Gmail's Authentication-Results header says the
// mail passed DKIM with a signature of domain or one of its subdomains,
// or passed DMARC for such a From domain. Only Gmail's own header counts:
// the first, which names mx.google.com; a sender can add others below it.
func signedBy(auth, domain string) bool {
	parts := strings.Split(auth, ";")
	if len(parts) < 2 || strings.TrimSpace(parts[0]) != "mx.google.com" {
		return false
	}
	within := func(d string) bool {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(d, "@"), "."))
		if i := strings.LastIndex(d, "@"); i >= 0 {
			d = d[i+1:]
		}
		return d == domain || strings.HasSuffix(d, "."+domain)
	}
	for _, p := range parts[1:] {
		fields := strings.Fields(p)
		if len(fields) == 0 {
			continue
		}
		method := strings.ToLower(fields[0])
		for _, f := range fields[1:] {
			k, v, _ := strings.Cut(f, "=")
			switch {
			case method == "dkim=pass" && (k == "header.i" || k == "header.d") && within(v),
				method == "dmarc=pass" && k == "header.from" && within(v):
				return true
			}
		}
	}
	return false
}

// extractTrades reads the trades of a broker's mail with a small model,
// with no tools: the mail is third-party text.
func (d Deps) extractTrades(ctx context.Context, in MailInput, chk mailCheck) (mailRelate, error) {
	var out mailRelate
	u, err := d.Store.UserByID(ctx, chk.UserID)
	if err != nil || u == nil {
		return out, fmt.Errorf("user %s: %v", chk.UserID, err)
	}
	as, err := d.Store.FinAccounts(ctx, u.ID)
	if err != nil {
		return out, err
	}
	e := agent.FinanceExtracting{UserName: u.DisplayName, Message: in.MailMessage}
	for _, a := range as {
		e.Accounts = append(e.Accounts, strings.TrimSpace(a.ID+" "+a.Broker+" "+a.Number))
	}
	prompt, err := agent.ExtractFinancePrompt(e)
	if err != nil {
		return out, err
	}
	turn, err := agent.RunTurn(ctx, d.Agent, protocol.TurnInput{UserID: u.ID, Text: prompt, UserName: u.DisplayName,
		Tools: protocol.ToolsNone, Model: agent.ExtractModel, OutputSchema: agent.ExtractedTradesSchema}, func(protocol.Packet) {})
	out.CostUSD = turn.CostUSD
	if err != nil {
		return out, err
	}
	out.Trades = &agent.ExtractedTrades{}
	if err := json.Unmarshal(turn.Structured, out.Trades); err != nil {
		return out, fmt.Errorf("extracted trades: %w", err)
	}
	return out, nil
}

// saveTrades checks the mail's rows and writes those that pass to the
// ledger, as the source mail:<address>/<message id>, whole: a mail read
// again replaces what it gave before. A report of the same days counts
// over them (store.SaveFinanceMail). Rows that fail are named in the
// user's finance channel, for them to upload the report.
func (d Deps) saveTrades(ctx context.Context, in MailInput, chk mailCheck, ext *agent.ExtractedTrades) (mailSave, error) {
	var out mailSave
	u, err := d.Store.UserByID(ctx, chk.UserID)
	if err != nil || u == nil {
		return out, fmt.Errorf("user %s: %v", chk.UserID, err)
	}
	if ext == nil || len(ext.Rows) == 0 {
		return out, d.Store.FinishMessage(ctx, in.Address, in.MessageID, store.MailSkipped)
	}
	doc := ext.Doc
	if _, ok := mailWindow[doc]; !ok {
		doc = finance.DocConfirmation
	}
	sent, err := time.Parse(time.RFC3339, in.Date)
	if err != nil {
		sent = d.now()
	}
	sent = sent.UTC()
	from, to := sent.Add(-mailWindow[doc]).Truncate(24*time.Hour), sent.Add(24*time.Hour)

	acct, err := d.mailAccount(ctx, u.ID, chk.Trusted, ext)
	if err != nil {
		return out, err
	}
	source := "mail:" + in.Address + "/" + in.MessageID
	var rows []finance.Row
	var problems []string
	for _, x := range ext.Rows {
		r := finance.Row{Date: x.Date, ISIN: strings.TrimSpace(x.ISIN), Name: x.Name, Symbol: x.Symbol, Exchange: x.Exchange,
			Side: x.Side, Quantity: x.Quantity, Price: x.Price, Amount: x.Amount, Fees: x.Fees, TaxWithheld: x.TaxWithheld,
			Currency: x.Currency, Ref: x.Ref}
		if !finance.ValidISIN(r.ISIN) {
			r.ISIN = "" // a model's guess is no ISIN: the row keeps its symbol or name
		}
		if ps := finance.CheckRow(r, from, to); len(ps) > 0 {
			problems = append(problems, fmt.Sprintf("%s %s %s: %s", x.Side, x.Quantity, cmp.Or(x.Symbol, x.Name, x.ISIN), strings.Join(ps, "; ")))
			continue
		}
		rows = append(rows, r)
	}
	if len(rows) > 0 {
		unmatched, err := d.Store.SaveFinanceMail(ctx, u.ID, acct.ID, source, doc, rows)
		if err != nil {
			return out, err
		}
		if len(unmatched) > 0 {
			slog.Info("broker mail: sells beyond what was bought", "user", u.ID, "message", in.MessageID, "unmatched", len(unmatched))
		}
	}
	out.Trades = len(rows)
	out.Dropped = problems
	if len(problems) > 0 {
		slog.Warn("mail trades dropped", "user", u.ID, "message", in.MessageID, "problems", problems)
		if err := d.tellFinance(ctx, u.ID, fmt.Sprintf("I couldn't read %d of the trades in <%s|%s>, so they aren't in your ledger:\n• %s\n"+
			"Upload the contract note or statement, and I'll import it.", len(problems), in.Link, in.Subject,
			strings.Join(problems, "\n• "))); err != nil {
			return out, err
		}
	}
	status := store.MailDone
	if len(rows) == 0 {
		status = store.MailSkipped
	}
	if err := d.Store.FinishMessage(ctx, in.Address, in.MessageID, status); err != nil {
		return out, err
	}
	if len(rows) > 0 && d.FinanceMailed != nil {
		if err := d.FinanceMailed(ctx, u); err != nil {
			slog.Error("after broker mail", "user", u.ID, "err", err)
		}
	}
	slog.Info("broker mail saved", "user", u.ID, "message", in.MessageID, "trades", len(rows), "dropped", len(problems))
	return out, nil
}

// mailAccount is the account a broker's mail is about: the one the model
// picked from the user's, else the account of its broker and number, made
// when new. A mail that names no broker is its trusted domain's.
func (d Deps) mailAccount(ctx context.Context, userID, trusted string, ext *agent.ExtractedTrades) (store.FinAccount, error) {
	if ext.Account != "" {
		a, err := d.Store.FinAccountOf(ctx, userID, ext.Account)
		if err != nil || a != nil {
			if a == nil {
				return store.FinAccount{}, err
			}
			return *a, err
		}
	}
	a, _, err := d.Store.EnsureFinAccount(ctx, userID, cmp.Or(strings.TrimSpace(ext.Broker), trusted), strings.TrimSpace(ext.AccountNumber), "")
	return a, err
}

// tellFinance posts text in the user's finance channel; without one, it
// is only logged.
func (d Deps) tellFinance(ctx context.Context, userID, text string) error {
	ch, err := d.Store.Channel(ctx, userID, store.KindFinance, "")
	if err != nil || ch == "" || d.Slack == nil {
		return err
	}
	_, _, err = d.Slack.PostMessageContext(ctx, ch, slackapi.MsgOptionText(text, false))
	return err
}
