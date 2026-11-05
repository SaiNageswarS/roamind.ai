package workflows

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/agent/protocol"
	"roamind.ai/connectors/market"
	"roamind.ai/memory/finance"
	"roamind.ai/memory/store"
)

// The finance workflows. The finance tools reconcile a user's trades as
// they import, so a turn's answer sees them; finance-reconcile catches up
// on any user left dirty, serialized per user.
const (
	// FinanceReconcileName settles a user's trade events into trades
	// (finance.ReconcileUser).
	FinanceReconcileName = "finance-reconcile"
	// FinanceChannelName gives a user with accounts their finance channel.
	FinanceChannelName = "finance-channel"
	// FinanceMarketName fetches rates, prices and splits once a day, for
	// every instrument any user holds.
	FinanceMarketName = "finance-market"
)

// FinanceMarketHour is when, in the server's zone, finance-market runs:
// after the US close of the day before.
var FinanceMarketHour = 7

// FinanceUserInput names a user.
type FinanceUserInput struct {
	UserID string `json:"user_id"`
}

// FinanceChannelOutput names the channel a run created.
type FinanceChannelOutput struct {
	Created string `json:"created,omitempty"` // channel name
}

// FinanceReconcileOutput is what a reconcile did.
type FinanceReconcileOutput struct {
	Confirmed   int `json:"confirmed"`
	Provisional int `json:"provisional"`
	Void        int `json:"void"`
	Notices     int `json:"notices"`
	Conflicts   int `json:"conflicts"`
}

func perUser() hatchet.TaskOption {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	return hatchet.WithConcurrency(&hatchet.Concurrency{Expression: "input.user_id", MaxRuns: &maxRuns, LimitStrategy: &strategy})
}

// FinanceReconcile declares the finance-reconcile task.
func FinanceReconcile(c *hatchet.Client, d Deps) *hatchet.StandaloneTask {
	return c.NewStandaloneTask(FinanceReconcileName, func(ctx hatchet.Context, in FinanceUserInput) (FinanceReconcileOutput, error) {
		res, err := finance.ReconcileUser(ctx, d.Finance, d.Store, in.UserID)
		return FinanceReconcileOutput{Confirmed: res.Confirmed, Provisional: res.Provisional, Void: res.Void,
			Notices: len(res.Notices), Conflicts: len(res.Conflicts)}, err
	},
		hatchet.WithWorkflowDescription("Settle a user's trade events into trades"),
		perUser(),
		hatchet.WithRetries(3),
		hatchet.WithExecutionTimeout(2*time.Minute),
	)
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
		"contract notes, holdings) and I'll keep one ledger across your accounts: holdings, entry and exit points, " +
		"profit and dividends. Ask me about them here, no @mention needed."
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

// financeTrigger returns a hook for chat-turn: after a turn it reconciles
// a user left dirty, and opens the finance channel of a user who has an
// account but no channel. The checks read only SQLite.
func financeTrigger(reconcile, channel *hatchet.StandaloneTask, d Deps) func(ctx context.Context, u *store.User, _ FinishedTurn) error {
	return func(ctx context.Context, u *store.User, _ FinishedTurn) error {
		mark, err := d.Store.FinanceDirty(ctx, u.ID)
		if err != nil {
			return err
		}
		var errs []error
		if mark != 0 {
			_, err := reconcile.RunNoWait(ctx, FinanceUserInput{UserID: u.ID})
			errs = append(errs, err)
		}
		ch, err := d.Store.Channel(ctx, u.ID, store.KindFinance, "")
		if err != nil || ch != "" || u.SlackUserID == "" {
			return errors.Join(append(errs, err)...)
		}
		as, err := d.Store.FinAccounts(ctx, u.ID)
		if err == nil && len(as) > 0 {
			_, err = channel.RunNoWait(ctx, FinanceUserInput{UserID: u.ID})
		}
		return errors.Join(append(errs, err)...)
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
	cs, err := d.Store.FinanceConflicts(ctx, userID, store.ConflictOpen)
	ref.Conflicts = len(cs)
	return ref, err
}

// FinanceMarketOutput is what a finance-market run fetched.
type FinanceMarketOutput struct {
	Rates    int      `json:"rates"`
	Prices   int      `json:"prices"`
	Splits   int      `json:"splits"`
	Listings int      `json:"listings"`
	Problems []string `json:"problems,omitempty"`
}

// FinanceMarket declares the daily finance-market task. It also starts a
// reconcile for any user left dirty.
func FinanceMarket(c *hatchet.Client, d Deps, reconcile *hatchet.StandaloneTask) *hatchet.StandaloneTask {
	return c.NewStandaloneTask(FinanceMarketName, func(ctx hatchet.Context, _ struct{}) (FinanceMarketOutput, error) {
		out, err := d.fetchMarket(ctx, d.now())
		if err != nil {
			return out, err
		}
		dirty, err := d.Store.DirtyFinanceUsers(ctx)
		for _, id := range dirty {
			if _, err := reconcile.RunNoWait(topLevel(ctx), FinanceUserInput{UserID: id}); err != nil {
				return out, err
			}
		}
		return out, err
	},
		hatchet.WithWorkflowDescription("Fetch rates, prices and splits for the instruments users hold"),
		hatchet.WithWorkflowCron(LocalCrons(d.Location, time.Now(), FinanceMarketHour)...),
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(15*time.Minute),
	)
}

// fetchMarket fetches what's new since the last run: the ECB's rates, and
// for every instrument held, its listing (found once by ISIN on EODHD),
// daily bars and splits, or for an Indian fund, its NAV from AMFI. A
// failure for one instrument is a problem in the output, not an error.
func (d Deps) fetchMarket(ctx context.Context, now time.Time) (FinanceMarketOutput, error) {
	var out FinanceMarketOutput
	db, mk := d.Finance, d.Market
	held, err := db.HeldISINs(ctx)
	if err != nil || len(held) == 0 {
		return out, err
	}
	first := now
	for _, t := range held {
		if t.Before(first) {
			first = t
		}
	}
	// Rates: from the day after the last one, or a few days before the
	// first trade, for its weekend.
	from := first.AddDate(0, 0, -10)
	if last, err := db.LastFXDay(ctx, finance.SourceECB); err != nil {
		return out, err
	} else if !last.IsZero() {
		from = last.AddDate(0, 0, 1)
	}
	rates, err := mk.ECBRates(ctx, from)
	if err != nil {
		out.Problems = append(out.Problems, err.Error())
	}
	fx := make([]finance.FX, len(rates))
	for i, r := range rates {
		fx[i] = finance.FX{Date: r.Day, Pair: "EUR" + r.Currency, Source: finance.SourceECB, Rate: r.Rate}
	}
	if err := db.PutFX(ctx, fx); err != nil {
		return out, err
	}
	out.Rates = len(fx)

	ins, err := db.Instruments(ctx)
	if err != nil {
		return out, err
	}
	listings, err := db.Listings(ctx)
	if err != nil {
		return out, err
	}
	lastDays, err := db.LastPriceDays(ctx)
	if err != nil {
		return out, err
	}
	var navs map[string]market.NAV
	for isin, since := range held {
		in := ins[isin]
		if strings.HasPrefix(isin, "INF") {
			if navs == nil {
				if navs, err = mk.AMFINAVs(ctx); err != nil {
					out.Problems = append(out.Problems, err.Error())
					navs = map[string]market.NAV{}
				}
			}
			n, ok := navs[isin]
			if !ok {
				continue
			}
			if len(listings[isin]) == 0 {
				if err := db.PutListings(ctx, []finance.Listing{{ISIN: isin, MIC: "AMFI", Symbol: n.Scheme, Currency: "INR"}}); err != nil {
					return out, err
				}
				out.Listings++
			}
			if err := db.PutPrices(ctx, []finance.Price{{Date: n.Day, ISIN: isin, MIC: "AMFI", Close: n.NAV}}); err != nil {
				return out, err
			}
			out.Prices++
			continue
		}
		if mk.EODHDKey == "" {
			continue
		}
		l, err := d.listing(ctx, in, isin, listings[isin])
		if err != nil {
			out.Problems = append(out.Problems, isin+": "+err.Error())
			continue
		}
		if len(listings[isin]) == 0 {
			out.Listings++
		}
		start := since.AddDate(0, 0, -7)
		if last := lastDays[[2]string{isin, l.MIC}]; !last.IsZero() {
			start = last.AddDate(0, 0, 1)
		}
		if start.After(now) {
			continue
		}
		bars, err := mk.EOD(ctx, l.Symbol, l.MIC, start)
		if err != nil {
			out.Problems = append(out.Problems, isin+": "+err.Error())
			continue
		}
		ps := make([]finance.Price, len(bars))
		for i, b := range bars {
			ps[i] = finance.Price{Date: b.Day, ISIN: isin, MIC: l.MIC, Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: b.Volume}
		}
		if err := db.PutPrices(ctx, ps); err != nil {
			return out, err
		}
		out.Prices += len(ps)
		splits, err := mk.Splits(ctx, l.Symbol, l.MIC, since)
		if err != nil {
			out.Problems = append(out.Problems, isin+": "+err.Error())
			continue
		}
		as := make([]finance.CorporateAction, len(splits))
		for i, s := range splits {
			as[i] = finance.CorporateAction{Date: s.Day, ISIN: isin, Kind: finance.ActionSplit, Factor: s.Factor, Source: "eodhd"}
		}
		if err := db.PutCorporateActions(ctx, as); err != nil {
			return out, err
		}
		out.Splits += len(as)
	}
	return out, nil
}

// homeExchange is the EODHD exchange an ISIN's prices are taken from, by
// its market, when it trades there in its currency.
var homeExchange = map[string][]string{"US": {"US"}, "IN": {"NSE", "BSE"}}

// listing is the instrument's listing whose prices value it: the one
// known, or else the EODHD listing in its currency, on its home exchange
// when it has one, which becomes its main listing.
func (d Deps) listing(ctx context.Context, in finance.Instrument, isin string, known []finance.Listing) (finance.Listing, error) {
	if len(known) > 0 {
		for _, l := range known {
			if l.MIC == in.MainMIC {
				return l, nil
			}
		}
		return known[0], nil
	}
	found, err := d.Market.Search(ctx, isin)
	if err != nil {
		return finance.Listing{}, err
	}
	cur := cmp.Or(in.Currency, map[string]string{"US": "USD", "IN": "INR"}[finance.MarketOf(isin)])
	best := -1
	for i, l := range found {
		if cur != "" && !strings.EqualFold(l.Currency, cur) {
			continue
		}
		if best < 0 {
			best = i
		}
		for _, ex := range homeExchange[finance.MarketOf(isin)] {
			if l.Exchange == ex {
				best = i
			}
		}
	}
	if best < 0 {
		return finance.Listing{}, fmt.Errorf("no listing in %s on EODHD", cmp.Or(cur, "its currency"))
	}
	f := found[best]
	l := finance.Listing{ISIN: isin, MIC: f.Exchange, Symbol: f.Code, Currency: strings.ToUpper(f.Currency)}
	if err := d.Finance.PutListings(ctx, []finance.Listing{l}); err != nil {
		return l, err
	}
	in.ISIN, in.MainMIC = isin, l.MIC
	in.Name = cmp.Or(in.Name, f.Name)
	in.Currency = cmp.Or(in.Currency, l.Currency)
	in.Market = cmp.Or(in.Market, finance.MarketOf(isin))
	if in.Kind == "" {
		in.Kind = map[string]string{"ETF": "etf", "FUND": "fund", "Common Stock": "stock"}[f.Type]
	}
	return l, d.Finance.PutInstruments(ctx, []finance.Instrument{in})
}
