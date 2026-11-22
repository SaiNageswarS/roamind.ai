// Package finance is the finance ledger's logic that isn't SQL: checking
// the rows a report or a broker's mail gives (Row), and matching sells to
// buys first in, first out (MatchFIFO). The ledger itself is in the
// Postgres store (memory/store, 12_finance.sql); holdings, cost and gains
// are queries on it.
package finance

import (
	"cmp"
	"math/big"
	"slices"
	"time"
)

// Sides of a transaction.
const (
	SideBuy         = "buy"
	SideSell        = "sell"
	SideVest        = "vest"
	SideESPP        = "espp"
	SideDividend    = "dividend"
	SideTransferIn  = "transfer_in"
	SideTransferOut = "transfer_out"
)

// Sides are every side, as the tools name them.
var Sides = []string{SideBuy, SideSell, SideVest, SideESPP, SideDividend, SideTransferIn, SideTransferOut}

// Acquires reports whether side adds shares: a buy, a vest, an ESPP
// purchase or a transfer in.
func Acquires(side string) bool {
	return side == SideBuy || side == SideVest || side == SideESPP || side == SideTransferIn
}

// Disposes reports whether side takes shares away: a sell or a transfer
// out.
func Disposes(side string) bool { return side == SideSell || side == SideTransferOut }

// Kinds of document a transaction comes from.
const (
	DocTradebook    = "tradebook"
	DocContractNote = "contract_note"
	DocStatement    = "statement"
	DocConfirmation = "confirmation"
)

// Txn is a transaction as MatchFIFO reads it.
type Txn struct {
	ID         int64
	Account    string
	Instrument string
	Date       time.Time
	Side       string
	Quantity   *big.Rat
}

// Match is shares of a sell matched to the buy they came from. BuyID is 0
// for shares sold beyond what was bought.
type Match struct {
	SellID   int64
	BuyID    int64
	Quantity *big.Rat
}

// MatchFIFO matches each sell to the oldest shares still held of its
// account and instrument. On one day, buys come before sells, so a day
// trade is matched; otherwise transactions are in ID order. Sides that
// neither acquire nor dispose (dividends) and transactions without a
// quantity are skipped. The result depends only on the input.
func MatchFIFO(txns []Txn) []Match {
	txns = slices.Clone(txns)
	slices.SortFunc(txns, func(a, b Txn) int {
		return cmp.Or(cmp.Compare(a.Account, b.Account), cmp.Compare(a.Instrument, b.Instrument),
			a.Date.Compare(b.Date), cmp.Compare(order(a.Side), order(b.Side)), cmp.Compare(a.ID, b.ID))
	})
	type lot struct {
		id   int64
		left *big.Rat
	}
	var out []Match
	var lots []lot // the open lots of the current account and instrument, oldest first
	var key [2]string
	for _, t := range txns {
		if t.Quantity == nil || t.Quantity.Sign() <= 0 || order(t.Side) == 0 {
			continue
		}
		if k := [2]string{t.Account, t.Instrument}; k != key {
			key, lots = k, nil
		}
		if Acquires(t.Side) {
			lots = append(lots, lot{t.ID, new(big.Rat).Set(t.Quantity)})
			continue
		}
		need := new(big.Rat).Set(t.Quantity)
		for need.Sign() > 0 && len(lots) > 0 {
			q := need
			if lots[0].left.Cmp(need) < 0 {
				q = lots[0].left
			}
			q = new(big.Rat).Set(q)
			out = append(out, Match{SellID: t.ID, BuyID: lots[0].id, Quantity: q})
			need.Sub(need, q)
			lots[0].left.Sub(lots[0].left, q)
			if lots[0].left.Sign() == 0 {
				lots = lots[1:]
			}
		}
		if need.Sign() > 0 {
			out = append(out, Match{SellID: t.ID, Quantity: need})
		}
	}
	return out
}

// order sorts a day's transactions: acquisitions, then disposals; 0 for
// sides that are neither.
func order(side string) int {
	switch {
	case Acquires(side):
		return 1
	case Disposes(side):
		return 2
	}
	return 0
}
