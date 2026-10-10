// Package identity derives stable user ids from email addresses and decides
// who may use the agent.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// NormalizeEmail lowercases and trims an email address.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// UserID is the primary user identity: hex(sha256(normalized email)). It is a
// stable identifier, not an anonymizer — emails are guessable.
func UserID(email string) string {
	sum := sha256.Sum256([]byte(NormalizeEmail(email)))
	return hex.EncodeToString(sum[:])
}

// Allowlist gates access by email. "*" allows everyone; an empty list allows
// no one.
type Allowlist struct {
	all    bool
	emails map[string]bool
}

// ParseAllowlist parses a comma-separated list of emails.
func ParseAllowlist(s string) Allowlist {
	a := Allowlist{emails: map[string]bool{}}
	for _, e := range strings.Split(s, ",") {
		e = NormalizeEmail(e)
		switch e {
		case "":
		case "*":
			a.all = true
		default:
			a.emails[e] = true
		}
	}
	return a
}

// Allowed reports whether email may use the agent.
func (a Allowlist) Allowed(email string) bool {
	return a.all || a.emails[NormalizeEmail(email)]
}
