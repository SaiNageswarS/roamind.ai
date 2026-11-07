package finance

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

// decimal is what ParseDec accepts: digits with an optional sign and point;
// no exponent, fraction, base prefix, separators or underscores.
var decimal = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)$`)

// ParseDec parses a decimal ("1512.35", "-3"; "1e3", "1,000" and "0x10"
// are refused), nil for "".
func ParseDec(s string) (*big.Rat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if !decimal.MatchString(s) {
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

// trimDecimal drops the trailing zeros of a decimal's text, such as those
// QuestDB pads a DECIMAL's scale with: 12.34500000 is 12.345, 3.00000000
// is 3.
func trimDecimal(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
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

// sub is a − b, treating nil as 0; nil when both are.
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

// sum and diff are a + b and a − b, nil when either is unknown (nil):
// for figures that can't be known without both, such as a cost.
func sum(a, b *big.Rat) *big.Rat {
	if a == nil || b == nil {
		return nil
	}
	return new(big.Rat).Add(a, b)
}

func diff(a, b *big.Rat) *big.Rat {
	if a == nil || b == nil {
		return nil
	}
	return new(big.Rat).Sub(a, b)
}

// orZero is r, or 0 for nil: for figures whose absence means none, such as
// fees.
func orZero(r *big.Rat) *big.Rat {
	if r == nil {
		return zero()
	}
	return r
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
