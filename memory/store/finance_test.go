package store

import (
	"context"
	"testing"
)

func TestFinAccounts(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	jobsUsers(t, s, "a", "b")
	a1, created, err := s.EnsureFinAccount(ctx, "a", " Zerodha ", "XX1234", "inr")
	if err != nil || !created || a1.ID != "a1" || a1.Broker != "zerodha" || a1.Currency != "INR" {
		t.Fatalf("first account = %+v %v %v", a1, created, err)
	}
	again, created, err := s.EnsureFinAccount(ctx, "a", "ZERODHA", "XX1234", "")
	if err != nil || created || again != a1 {
		t.Fatalf("same broker and number = %+v %v %v", again, created, err)
	}
	a2, _, _ := s.EnsureFinAccount(ctx, "a", "zerodha", "XX9999", "")
	b1, _, _ := s.EnsureFinAccount(ctx, "b", "zerodha", "XX1234", "")
	if a2.ID != "a2" || b1.ID != "a1" || b1.UserID != "b" {
		t.Fatalf("a2 = %+v, b's = %+v", a2, b1)
	}
	if ok, err := s.NameFinAccount(ctx, "a", "a2", "Kite"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := s.NameFinAccount(ctx, "b", "a2", "x"); ok {
		t.Error("b named a's account")
	}
	if got, _ := s.FinAccountOf(ctx, "a", "kite"); got == nil || got.ID != "a2" || got.Label() != "Kite" {
		t.Errorf("by name = %+v", got)
	}
	if got, _ := s.FinAccountOf(ctx, "a", "A1"); got == nil || got.Label() != "zerodha XX1234" {
		t.Errorf("by id = %+v", got)
	}
	if got, _ := s.FinAccountOf(ctx, "b", "kite"); got != nil {
		t.Errorf("b sees a's account %+v", got)
	}
	if as, _ := s.FinAccounts(ctx, "a"); len(as) != 2 {
		t.Errorf("a's accounts = %+v", as)
	}
}

func TestFinanceUploadsAndDirty(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	jobsUsers(t, s, "a", "b")
	if u, err := s.FinanceUploadOf(ctx, "a", "h1"); u != nil || err != nil {
		t.Fatal(u, err)
	}
	up := FinanceUpload{UserID: "a", SHA256: "h1", File: "tb.csv", Account: "a1", Doc: "tradebook", PeriodFrom: "2026-10-01", PeriodTo: "2026-10-31", Rows: 3}
	if err := s.SaveFinanceUpload(ctx, up); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFinanceUpload(ctx, up); err == nil {
		t.Error("the same file was imported twice")
	}
	if u, _ := s.FinanceUploadOf(ctx, "a", "h1"); u == nil || u.PeriodTo != "2026-10-31" || u.Rows != 3 {
		t.Errorf("upload = %+v", u)
	}
	if u, _ := s.FinanceUploadOf(ctx, "b", "h1"); u != nil {
		t.Error("b sees a's upload")
	}

	mark, err := s.MarkFinanceDirty(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	later, _ := s.MarkFinanceDirty(ctx, "a")
	// A reconcile that started before the second change leaves a dirty.
	if err := s.ClearFinanceDirty(ctx, "a", mark); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FinanceDirty(ctx, "a"); got != later {
		t.Errorf("dirty = %d, want %d", got, later)
	}
	if ids, _ := s.DirtyFinanceUsers(ctx); len(ids) != 1 || ids[0] != "a" {
		t.Errorf("dirty users = %v", ids)
	}
	_ = s.ClearFinanceDirty(ctx, "a", later)
	if got, _ := s.FinanceDirty(ctx, "a"); got != 0 {
		t.Errorf("still dirty: %d", got)
	}
}

func TestFinanceConflicts(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	jobsUsers(t, s, "a", "b")
	c := func(isin, detail string) FinanceConflict {
		return FinanceConflict{Account: "a1", ISIN: isin, Day: "2026-10-31", Kind: ConflictHoldings, Detail: detail}
	}
	if err := s.SyncFinanceConflicts(ctx, "a", []FinanceConflict{c("X1", "120 vs 110"), c("X2", "5 vs 4")}); err != nil {
		t.Fatal(err)
	}
	open, _ := s.FinanceConflicts(ctx, "a", ConflictOpen)
	if len(open) != 2 {
		t.Fatalf("open = %+v", open)
	}
	var x2 int64
	for _, o := range open {
		if o.ISIN == "X2" {
			x2 = o.ID
		}
	}
	if ok, _ := s.DismissFinanceConflict(ctx, "b", x2); ok {
		t.Error("b dismissed a's conflict")
	}
	if ok, err := s.DismissFinanceConflict(ctx, "a", x2); !ok || err != nil {
		t.Fatal(ok, err)
	}
	// X1 now agrees; X2 still differs, and stays dismissed.
	if err := s.SyncFinanceConflicts(ctx, "a", []FinanceConflict{c("X2", "6 vs 4")}); err != nil {
		t.Fatal(err)
	}
	all, _ := s.FinanceConflicts(ctx, "a", "")
	states := map[string]string{}
	for _, o := range all {
		states[o.ISIN] = o.State + " " + o.Detail
	}
	if states["X1"] != "resolved 120 vs 110" || states["X2"] != "dismissed 6 vs 4" {
		t.Errorf("states = %v", states)
	}
	if got, _ := s.FinanceConflicts(ctx, "b", ""); len(got) != 0 {
		t.Errorf("b's conflicts = %+v", got)
	}
}

func TestBaseCurrency(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	jobsUsers(t, s, "a")
	if c, _ := s.BaseCurrency(ctx, "a"); c != "EUR" {
		t.Errorf("default = %s", c)
	}
	if err := s.SetBaseCurrency(ctx, "a", "inr"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.BaseCurrency(ctx, "a"); c != "INR" {
		t.Errorf("set = %s", c)
	}
}
