package finance

import (
	"fmt"
	"math/big"
	"strings"
)

// ParseDec parses a decimal ("1512.35", "-3", "1e3" is refused), nil for "".
func ParseDec(s string) (*big.Rat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if strings.ContainsAny(s, "eE/") {
		return nil, fmt.Errorf("%q is not a decimal", s)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("%q is not a decimal", s)
	}
	return r, nil
}

// dec parses a decimal already checked, nil for "" or a bad one.
func dec(s string) *big.Rat {
	r, _ := ParseDec(s)
	return r
}

// DecText is r with at most places decimals, without trailing zeros; "" for
// nil.
func DecText(r *big.Rat, places int) string {
	if r == nil {
		return ""
	}
	return trimDecimal(r.FloatString(places))
}

// add is a + b, treating nil as 0; nil when both are.
func add(a, b *big.Rat) *big.Rat {
	switch {
	case a == nil && b == nil:
		return nil
	case a == nil:
		return new(big.Rat).Set(b)
	case b == nil:
		return new(big.Rat).Set(a)
	}
	return new(big.Rat).Add(a, b)
}

func sub(a, b *big.Rat) *big.Rat {
	if b == nil {
		return add(a, nil)
	}
	return add(a, new(big.Rat).Neg(b))
}

func mul(a, b *big.Rat) *big.Rat {
	if a == nil || b == nil {
		return nil
	}
	return new(big.Rat).Mul(a, b)
}

// div is a / b, nil when either is nil or b is 0.
func div(a, b *big.Rat) *big.Rat {
	if a == nil || b == nil || b.Sign() == 0 {
		return nil
	}
	return new(big.Rat).Quo(a, b)
}

func zero() *big.Rat { return new(big.Rat) }

// near reports whether a and b differ by at most frac of b's size.
func near(a, b *big.Rat, frac *big.Rat) bool {
	if a == nil || b == nil {
		return false
	}
	diff := new(big.Rat).Abs(new(big.Rat).Sub(a, b))
	return diff.Cmp(new(big.Rat).Mul(new(big.Rat).Abs(b), frac)) <= 0
}

func eq(a, b *big.Rat) bool { return a != nil && b != nil && a.Cmp(b) == 0 }

// Tolerances, as fractions.
var (
	// copyTolerance is how close two copies of one fill's price are.
	copyTolerance = big.NewRat(1, 1000)
	// agreeTolerance is how close mail's average price is to a report's
	// for them to agree: fees and rounding differ between documents.
	agreeTolerance = big.NewRat(5, 1000)
	// amountTolerance is how close quantity × price is to a row's amount.
	amountTolerance = big.NewRat(1, 100)
)
