package identity

import "testing"

func TestUserIDNormalizes(t *testing.T) {
	if UserID(" A@X.com ") != UserID("a@x.com") {
		t.Fatal("not normalized")
	}
	if len(UserID("a@x.com")) != 64 {
		t.Fatal("want hex sha256")
	}
}

func TestAllowlist(t *testing.T) {
	a := ParseAllowlist("a@x.com, B@y.com")
	if !a.Allowed("A@x.com") || !a.Allowed("b@y.com") || a.Allowed("c@z.com") {
		t.Fatal("allowlist mismatch")
	}
	if ParseAllowlist("").Allowed("a@x.com") {
		t.Fatal("empty list must deny")
	}
	if !ParseAllowlist("*").Allowed("anyone@x.com") {
		t.Fatal("* must allow")
	}
}
