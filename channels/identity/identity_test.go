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
