package settings

import (
	"context"
	"testing"
)

func TestRequestCredential(t *testing.T) {
	ctx := context.Background()

	t.Run("no function configured", func(t *testing.T) {
		_, _, ok := Settings{}.RequestCredential(ctx, "https", "h", "/p")
		if ok {
			t.Error("expected ok=false with no function")
		}
	})

	t.Run("function declines", func(t *testing.T) {
		s := Settings{HTTPCredentialRequest: func(context.Context, string, string, string) (*string, *string, bool) {
			return nil, nil, false
		}}
		if _, _, ok := s.RequestCredential(ctx, "https", "h", "/p"); ok {
			t.Error("expected ok=false when the function declines")
		}
	})

	t.Run("returns credentials and passes args through", func(t *testing.T) {
		user, pass := "u", "p"
		s := Settings{HTTPCredentialRequest: func(_ context.Context, protocol, host, path string) (*string, *string, bool) {
			if protocol != "https" || host != "github.com" || path != "/org/repo" {
				t.Errorf("unexpected args: %q %q %q", protocol, host, path)
			}
			return &user, &pass, true
		}}
		gotUser, gotPass, ok := s.RequestCredential(ctx, "https", "github.com", "/org/repo")
		if !ok || gotUser != "u" || gotPass != "p" {
			t.Errorf("got (%q, %q, %v), want (u, p, true)", gotUser, gotPass, ok)
		}
	})

	t.Run("nil pointers normalise to empty strings", func(t *testing.T) {
		s := Settings{HTTPCredentialRequest: func(context.Context, string, string, string) (*string, *string, bool) {
			return nil, nil, true
		}}
		user, pass, ok := s.RequestCredential(ctx, "https", "h", "/p")
		if !ok || user != "" || pass != "" {
			t.Errorf("got (%q, %q, %v), want empty strings and true", user, pass, ok)
		}
	})
}

func TestMatchHTTPSCredential_FiltersByURLUsername(t *testing.T) {
	s := Settings{
		HTTPSCredentials: []HTTPSCredential{
			{Host: "example.com", Username: "other", Password: "other-pass"},
			{Host: "example.com", Username: "acme", Password: "acme-pass"},
		},
	}

	t.Run("the named account is selected", func(t *testing.T) {
		got := s.MatchHTTPSCredential("https://acme@example.com/org/repo")
		if got == nil || got.Password != "acme-pass" {
			t.Fatalf("got %#v, want the credential for the named account", got)
		}
	})

	t.Run("no username in the URL matches on host alone", func(t *testing.T) {
		// Host-only credentials do not rank against each other, so the first one
		// configured wins — as with git's credential store. Unchanged by the
		// username filter.
		got := s.MatchHTTPSCredential("https://example.com/org/repo")
		if got == nil || got.Password != "other-pass" {
			t.Fatalf("got %#v, want the first host-only match", got)
		}
	})

	t.Run("every host-only match is offered, in configuration order", func(t *testing.T) {
		got := s.MatchHTTPSCredentials("https://example.com/org/repo")
		if len(got) != 2 || got[0].Password != "other-pass" || got[1].Password != "acme-pass" {
			t.Fatalf("got %#v, want both credentials in configuration order", got)
		}
	})

	t.Run("a credential for another account does not match", func(t *testing.T) {
		// git refuses to pair a stored password with an account it was not
		// stored against, and so do we.
		got := s.MatchHTTPSCredential("https://nobody@example.com/org/repo")
		if got != nil {
			t.Fatalf("got %#v, want no match", got)
		}
	})

	t.Run("a credential with no username does not match a named request", func(t *testing.T) {
		anon := Settings{HTTPSCredentials: []HTTPSCredential{
			{Host: "example.com", Password: "pass"},
		}}
		if got := anon.MatchHTTPSCredential("https://acme@example.com/org/repo"); got != nil {
			t.Fatalf("got %#v, want no match", got)
		}
		// ...but still matches when no account is named.
		if got := anon.MatchHTTPSCredential("https://example.com/org/repo"); got == nil {
			t.Fatal("expected the host credential to match an unnamed request")
		}
	})

	t.Run("path specificity still applies within the named account", func(t *testing.T) {
		s := Settings{HTTPSCredentials: []HTTPSCredential{
			{Host: "h", Username: "u", Password: "broad"},
			{Host: "h", Username: "u", Password: "narrow", Path: "/org/repo"},
		}}
		got := s.MatchHTTPSCredential("https://u@h/org/repo/sub")
		if got == nil || got.Password != "narrow" {
			t.Fatalf("got %#v, want the longest path prefix", got)
		}
	})
}
