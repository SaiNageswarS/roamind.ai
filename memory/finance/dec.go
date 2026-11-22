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

// DecText is r with at most places decimals, without trailing zeros; "" for
// nil.
func DecText(r *big.Rat, places int) string {
	if r == nil {
		return ""
	}
	s := r.FloatString(places)
	if !strings.Contains(s, ".") {
		return s
	}
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
}
