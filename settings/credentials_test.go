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

func TestMatchHTTPSCredential_PrefersURLUsername(t *testing.T) {
	s := Settings{
		HTTPSCredentials: []HTTPSCredential{
			{Host: "dev.azure.com", Username: "other", Password: "other-pass"},
			{Host: "dev.azure.com", Username: "acme", Password: "acme-pass"},
		},
	}

	t.Run("named account wins", func(t *testing.T) {
		got := s.MatchHTTPSCredential("https://acme@dev.azure.com/acme/DevOps/_git/x")
		if got == nil || got.Password != "acme-pass" {
			t.Fatalf("got %#v, want the credential for the named account", got)
		}
	})

	t.Run("no username in URL keeps existing behaviour", func(t *testing.T) {
		// Host-only credentials do not rank against each other, so the last one
		// configured wins. The username preference does not disturb that.
		got := s.MatchHTTPSCredential("https://dev.azure.com/acme/DevOps/_git/x")
		if got == nil || got.Password != "acme-pass" {
			t.Fatalf("got %#v, want the last host-only match", got)
		}
	})

	t.Run("unmatched username still falls back to the host credential", func(t *testing.T) {
		// The configured account often differs from the URL's — a PAT stored
		// under x-access-token, say. A non-match must not mean no credential.
		one := Settings{HTTPSCredentials: []HTTPSCredential{
			{Host: "dev.azure.com", Username: "x-access-token", Password: "pat"},
		}}
		got := one.MatchHTTPSCredential("https://acme@dev.azure.com/acme/DevOps/_git/x")
		if got == nil || got.Password != "pat" {
			t.Fatalf("got %#v, want the host credential", got)
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
