package store

import (
	"context"
	"testing"
)

func TestViewStateRoundTrip(t *testing.T) {
	s, path := openTemp(t)
	ctx := context.Background()
	if _, ok, err := s.ViewState(ctx, "cost"); err != nil || ok {
		t.Fatalf("empty store: ok %v err %v, want nothing saved", ok, err)
	}
	for _, d := range []string{`{"range":"7d"}`, `{"range":"90d"}`} {
		if err := s.SaveViewState(ctx, "cost", []byte(d)); err != nil {
			t.Fatalf("SaveViewState: %v", err)
		}
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	got, ok, err := s2.ViewState(ctx, "cost")
	if err != nil || !ok || string(got) != `{"range":"90d"}` {
		t.Errorf("after a reopen: %q ok %v err %v, want the newest save", got, ok, err)
	}
	if _, ok, _ := s2.ViewState(ctx, "wall"); ok {
		t.Error("another key should have nothing saved")
	}
}
