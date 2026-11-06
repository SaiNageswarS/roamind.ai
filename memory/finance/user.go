package finance

import (
	"context"
	"time"

	"roamind.ai/memory/store"
)

// ReconcileUser settles the user's trade events into trades (Reconcile)
// and returns how many there are. Calls for one user must not overlap (the
// finance-reconcile task's concurrency); its output depends only on the
// events, so a later run fixes what an overlapping one wrote. A user whose
// events change while it runs stays dirty.
func ReconcileUser(ctx context.Context, d *DB, st *store.Store, userID string) (int, error) {
	mark, err := st.FinanceDirty(ctx, userID)
	if err != nil {
		return 0, err
	}
	if err := d.WaitApplied(ctx, "trade_events"); err != nil {
		return 0, err
	}
	events, err := d.TradeEvents(ctx, userID, epoch, time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		return 0, err
	}
	trades := Reconcile(events)
	if err := d.PutTrades(ctx, userID, trades); err != nil {
		return 0, err
	}
	if mark != 0 {
		if err := st.ClearFinanceDirty(ctx, userID, mark); err != nil {
			return 0, err
		}
	}
	return len(trades), d.WaitApplied(ctx, "trades")
}
