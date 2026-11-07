package finance

import "time"

// epoch is the fixed timestamp of reference rows (instruments, listings),
// which makes their other keys their key, and the start of all time for
// range reads.
var epoch = time.Unix(0, 0).UTC()

// forever is the end of all time for range reads.
var forever = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

// day is t as a calendar day, at midnight UTC.
func day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
