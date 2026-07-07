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
