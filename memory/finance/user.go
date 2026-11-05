package finance

import (
	"context"
	"fmt"
	"time"

	"roamind.ai/memory/store"
)

// Result is what a reconcile of one user did.
type Result struct {
	Confirmed, Provisional, Void int
	Notices                      []Notice
	// Conflicts are the user's open conflicts after it.
	Conflicts []store.FinanceConflict
}

// ReconcileUser settles the user's trade events into trades (Reconcile),
// with their uploads' periods as the coverage, then checks the trades
// against their latest reported holdings and makes the differences their
// conflicts. Calls for one user must not overlap (the finance-reconcile
// task's concurrency); its output depends only on the data, so a later run
// fixes what an overlapping one wrote. A user whose events change while it
// runs stays dirty.
func ReconcileUser(ctx context.Context, d *DB, st *store.Store, userID string) (Result, error) {
	var res Result
	mark, err := st.FinanceDirty(ctx, userID)
	if err != nil {
		return res, err
	}
	cover, err := coverage(ctx, st, userID)
	if err != nil {
		return res, err
	}
	if err := d.WaitApplied(ctx, "trade_events", "trades", "holdings_reported"); err != nil {
		return res, err
	}
	events, err := d.TradeEvents(ctx, userID, epoch, time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		return res, err
	}
	previous, err := d.Trades(ctx, userID)
	if err != nil {
		return res, err
	}
	trades, notices := Reconcile(events, cover, previous)
	if err := d.PutTrades(ctx, userID, trades); err != nil {
		return res, err
	}
	res.Notices = notices
	for _, t := range trades {
		switch t.State {
		case Confirmed:
			res.Confirmed++
		case Provisional:
			res.Provisional++
		case Void:
			res.Void++
		}
	}
	actions, err := d.Splits(ctx)
	if err != nil {
		return res, err
	}
	held, err := d.LatestHoldings(ctx, userID)
	if err != nil {
		return res, err
	}
	var conflicts []store.FinanceConflict
	for _, m := range CheckHoldings(trades, actions, held) {
		conflicts = append(conflicts, store.FinanceConflict{Account: m.Account, ISIN: m.ISIN, Day: m.Day.Format(time.DateOnly),
			Kind: store.ConflictHoldings, Detail: fmt.Sprintf("the report of %s shows %s; the trades give %s",
				m.Day.Format(time.DateOnly), DecText(m.Reported, 8), DecText(m.Ledger, 8))})
	}
	if err := st.SyncFinanceConflicts(ctx, userID, conflicts); err != nil {
		return res, err
	}
	if res.Conflicts, err = st.FinanceConflicts(ctx, userID, store.ConflictOpen); err != nil {
		return res, err
	}
	if mark != 0 {
		if err := st.ClearFinanceDirty(ctx, userID, mark); err != nil {
			return res, err
		}
	}
	return res, d.WaitApplied(ctx, "trades")
}

// coverage is the days the user's uploaded reports cover, by account.
func coverage(ctx context.Context, st *store.Store, userID string) (Coverage, error) {
	ups, err := st.FinanceUploads(ctx, userID)
	if err != nil {
		return nil, err
	}
	c := Coverage{}
	for _, u := range ups {
		if u.PeriodFrom == "" || u.PeriodTo == "" {
			continue
		}
		from, err1 := time.Parse(time.DateOnly, u.PeriodFrom)
		to, err2 := time.Parse(time.DateOnly, u.PeriodTo)
		if err1 != nil || err2 != nil {
			continue
		}
		c[u.Account] = append(c[u.Account], Period{From: from, To: to})
	}
	return c, nil
}
