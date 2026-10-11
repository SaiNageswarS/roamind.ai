// Package identity derives stable user ids from email addresses.
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
