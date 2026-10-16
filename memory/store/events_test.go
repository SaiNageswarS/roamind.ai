package store

import (
	"context"
	"testing"
)

func TestMarkEvent(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for i, want := range []bool{true, false} {
		fresh, err := s.MarkEvent(ctx, "Ev1")
		if err != nil || fresh != want {
			t.Fatalf("call %d: fresh=%v err=%v", i, fresh, err)
		}
	}
}
