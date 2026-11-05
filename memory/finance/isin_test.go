package finance

import "testing"

func TestValidISIN(t *testing.T) {
	for _, s := range []string{"US0378331005", "INE009A01021", "IE00B4L5Y983", "INF846K01WO1", "DE0007164600"} {
		if !ValidISIN(s) {
			t.Errorf("%s refused", s)
		}
	}
	for _, s := range []string{"US0378331006", "us0378331005", "US037833100", "INFY", "", "1S0378331005"} {
		if ValidISIN(s) {
			t.Errorf("%s accepted", s)
		}
	}
}
