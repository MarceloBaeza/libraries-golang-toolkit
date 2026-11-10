package telegram

import (
	"errors"
	"strings"
	"testing"
)

func TestFlowCallbackRoundTrip(t *testing.T) {
	s := &Session{FlowID: "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0", Revision: 7}
	data := FlowCallback(s, "fund:3")
	if len(data) > 64 {
		t.Fatalf("callback too long: %d", len(data))
	}
	c, err := ParseFlowCallback(data)
	if err != nil {
		t.Fatal(err)
	}
	if c.Choice != "fund:3" || !c.Matches(s) {
		t.Fatalf("unexpected %+v", c)
	}
	// The next revision invalidates the button.
	if c.Matches(&Session{FlowID: s.FlowID, Revision: 8}) {
		t.Fatal("old revision must not match")
	}
	// A different flow invalidates it too.
	if c.Matches(&Session{FlowID: "aaaaaaaa-4b5a-6978-8796-a5b4c3d2e1f0", Revision: 7}) {
		t.Fatal("other flow must not match")
	}
	if c.Matches(nil) {
		t.Fatal("nil session must not match")
	}
}

func TestParseFlowCallbackRejectsGarbage(t *testing.T) {
	for _, d := range []string{"", "f:", "f:a:1", "f::1:x", "f:a:-1:x", "f:a:x:y", "x:a:1:y", "f:a:1:"} {
		if _, err := ParseFlowCallback(d); !errors.Is(err, ErrBadCallback) {
			t.Errorf("%q: expected ErrBadCallback, got %v", d, err)
		}
	}
}

func TestLinkToken(t *testing.T) {
	tok, hash, err := NewLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidLinkToken(tok) {
		t.Fatalf("token has wrong shape: %q", tok)
	}
	if hash != HashLinkToken(tok) || len(hash) != 64 || strings.Contains(hash, tok) {
		t.Fatal("hash mismatch")
	}
	tok2, _, _ := NewLinkToken()
	if tok == tok2 {
		t.Fatal("tokens must be random")
	}
	link, err := DeepLink("@OmegaBot", tok)
	if err != nil || link != "https://t.me/OmegaBot?start="+tok {
		t.Fatalf("link %q err %v", link, err)
	}
	if _, err := DeepLink("", tok); err == nil {
		t.Fatal("empty bot must fail")
	}
	if _, err := DeepLink("bot", "short"); err == nil {
		t.Fatal("bad token must fail")
	}
	for _, bad := range []string{"", "abc", strings.Repeat("a", 42), strings.Repeat("a", 44), strings.Repeat("a", 42) + "!"} {
		if ValidLinkToken(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestStartPayload(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"/start abc", "abc", true},
		{"/start@OmegaBot abc", "abc", true},
		{"/start", "", true},
		{"  /start   xyz  ", "xyz", true},
		{"/menu abc", "", false},
		{"hola", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := StartPayload(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("%q: got %q,%v", c.in, got, ok)
		}
	}
}

func TestLargestPhoto(t *testing.T) {
	m := &Message{Photo: []PhotoSize{{FileID: "s", Width: 90, Height: 60}, {FileID: "l", Width: 1280, Height: 960}, {FileID: "m", Width: 320, Height: 240}}}
	p, ok := m.LargestPhoto()
	if !ok || p.FileID != "l" {
		t.Fatalf("got %+v", p)
	}
	if _, ok := (&Message{}).LargestPhoto(); ok {
		t.Fatal("no photo")
	}
}
