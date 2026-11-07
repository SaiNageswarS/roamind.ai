package finance

import "context"

// Dirty is where users whose trade events changed are marked, and the mark
// cleared once they are reconciled: the SQLite store (memory/store).
type Dirty interface {
	// FinanceDirty is the user's mark, 0 when they are clean.
	FinanceDirty(ctx context.Context, userID string) (int64, error)
	// ClearFinanceDirty clears it if it is still since.
	ClearFinanceDirty(ctx context.Context, userID string, since int64) error
}

// ReconcileUser settles the user's trade events into trades (Reconcile)
// and returns how many there are. Calls for one user must not overlap (the
// finance-reconcile task's concurrency); its output depends only on the
// events, so a later run fixes what an overlapping one wrote. A user whose
// events change while it runs stays dirty.
func ReconcileUser(ctx context.Context, d *DB, st Dirty, userID string) (int, error) {
	mark, err := st.FinanceDirty(ctx, userID)
	if err != nil {
		return 0, err
	}
	if err := d.WaitApplied(ctx, "trade_events"); err != nil {
		return 0, err
	}
	events, err := d.TradeEvents(ctx, userID, epoch, forever)
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
