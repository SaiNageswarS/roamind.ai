package finance

// ValidISIN reports whether s is an ISIN: two letters, nine letters or
// digits, and a check digit that matches (ISO 6166).
func ValidISIN(s string) bool {
	if len(s) != 12 {
		return false
	}
	var digits []int
	for i, c := range s {
		switch {
		case c >= '0' && c <= '9' && i >= 2:
			digits = append(digits, int(c-'0'))
		case c >= 'A' && c <= 'Z' && i < 11:
			n := int(c-'A') + 10
			digits = append(digits, n/10, n%10)
		default:
			return false
		}
	}
	// Luhn, over the digits with the letters spelled out.
	sum := 0
	for i := range digits {
		v := digits[len(digits)-1-i]
		if i%2 == 1 {
			if v *= 2; v > 9 {
				v -= 9
			}
		}
		sum += v
	}
	return sum%10 == 0
}
