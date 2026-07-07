package git

import (
	"context"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"

	"github.com/liamg/grabber/settings"
)

// stubCredentialFill temporarily replaces the system git credential helper for
// the duration of the test, recording whether it was consulted.
func stubCredentialFill(t *testing.T, ret *http.BasicAuth) *bool {
	t.Helper()
	called := false
	orig := credentialFillFunc
	credentialFillFunc = func(_ context.Context, _, _ string) *http.BasicAuth {
		called = true
		return ret
	}
	t.Cleanup(func() { credentialFillFunc = orig })
	return &called
}

func TestResolveAuth_SystemCredentialHelper(t *testing.T) {
	sentinel := &http.BasicAuth{Username: "helper-user", Password: "helper-pass"}

	t.Run("consulted when system fallback enabled", func(t *testing.T) {
		called := stubCredentialFill(t, sentinel)
		d := &Downloader{repoURL: "https://example.com/org/repo.git"}

		auth, err := d.resolveAuth(context.Background(), settings.Settings{})
		if err != nil {
			t.Fatalf("resolveAuth: %v", err)
		}
		if !*called {
			t.Fatal("expected the credential helper to be consulted")
		}
		ba, ok := auth.(*http.BasicAuth)
		if !ok || ba.Username != "helper-user" {
			t.Fatalf("expected helper credentials, got %#v", auth)
		}
	})

	t.Run("skipped when system fallback disabled", func(t *testing.T) {
		called := stubCredentialFill(t, sentinel)
		d := &Downloader{repoURL: "https://example.com/org/repo.git"}

		auth, err := d.resolveAuth(context.Background(), settings.Settings{NoSystemFallback: true})
		if err != nil {
			t.Fatalf("resolveAuth: %v", err)
		}
		if *called {
			t.Error("credential helper must not be consulted when system fallback is off")
		}
		if auth != nil {
			t.Errorf("expected nil auth, got %#v", auth)
		}
	})
}

func TestResolveAuth_DynamicCredentials(t *testing.T) {
	t.Run("used when no static credential matches, before the helper", func(t *testing.T) {
		called := stubCredentialFill(t, &http.BasicAuth{Username: "helper", Password: "helper"})
		var gotProto, gotHost, gotPath string
		s := settings.Settings{
			HTTPCredentialRequest: func(_ context.Context, protocol, host, path string) (*string, *string, bool) {
				gotProto, gotHost, gotPath = protocol, host, path
				u, p := "dyn", "dynpass"
				return &u, &p, true
			},
		}
		d := &Downloader{repoURL: "https://git.example.com/org/repo.git"}
		auth, err := d.resolveAuth(context.Background(), s)
		if err != nil {
			t.Fatalf("resolveAuth: %v", err)
		}
		ba, ok := auth.(*http.BasicAuth)
		if !ok || ba.Username != "dyn" || ba.Password != "dynpass" {
			t.Fatalf("expected dynamic credentials, got %#v", auth)
		}
		if *called {
			t.Error("system helper must not be consulted once the dynamic function returns credentials")
		}
		if gotProto != "https" || gotHost != "git.example.com" || gotPath != "/org/repo.git" {
			t.Errorf("callback args = (%q,%q,%q)", gotProto, gotHost, gotPath)
		}
	})

	t.Run("static credential wins over dynamic", func(t *testing.T) {
		s := settings.Settings{
			HTTPSCredentials: []settings.HTTPSCredential{{Host: "git.example.com", Username: "static", Password: "s"}},
			HTTPCredentialRequest: func(context.Context, string, string, string) (*string, *string, bool) {
				t.Error("dynamic function must not be consulted when a static credential matches")
				return nil, nil, false
			},
		}
		d := &Downloader{repoURL: "https://git.example.com/org/repo.git"}
		auth, err := d.resolveAuth(context.Background(), s)
		if err != nil {
			t.Fatalf("resolveAuth: %v", err)
		}
		if ba, _ := auth.(*http.BasicAuth); ba == nil || ba.Username != "static" {
			t.Errorf("expected static credentials, got %#v", auth)
		}
	})

	t.Run("declining function falls through to the helper", func(t *testing.T) {
		called := stubCredentialFill(t, &http.BasicAuth{Username: "helper", Password: "h"})
		s := settings.Settings{
			HTTPCredentialRequest: func(context.Context, string, string, string) (*string, *string, bool) {
				return nil, nil, false
			},
		}
		d := &Downloader{repoURL: "https://git.example.com/org/repo.git"}
		auth, err := d.resolveAuth(context.Background(), s)
		if err != nil {
			t.Fatalf("resolveAuth: %v", err)
		}
		if !*called {
			t.Error("expected the helper to be consulted after the dynamic function declined")
		}
		if ba, _ := auth.(*http.BasicAuth); ba == nil || ba.Username != "helper" {
			t.Errorf("expected helper credentials, got %#v", auth)
		}
	})
}

func TestResolveAuth_SSHAgentGatedByFallback(t *testing.T) {
	// SSH URL with no configured key: with the system fallback off, we must not
	// touch the SSH agent and should simply report no credentials.
	d := &Downloader{repoURL: "ssh://git@example.com/org/repo.git"}
	auth, err := d.resolveAuth(context.Background(), settings.Settings{NoSystemFallback: true})
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if auth != nil {
		t.Errorf("expected nil auth (no key, agent disabled), got %#v", auth)
	}
}

func TestResolveAuth_SSHKeyUsesHostKeyCallback(t *testing.T) {
	_, hostAuth := newTestHostKey(t)
	key, _ := generateSSHKeyPair(t)

	d := &Downloader{repoURL: "ssh://git@example.com/org/repo.git"}
	auth, err := d.resolveAuth(context.Background(), settings.Settings{
		Git: settings.GitConfig{
			SSHKeys:    []settings.SSHCredential{{Key: key}},
			KnownHosts: knownHostsLine("example.com", hostAuth),
		},
	})
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	pk, ok := auth.(*ssh.PublicKeys)
	if !ok {
		t.Fatalf("expected *ssh.PublicKeys, got %T", auth)
	}
	if pk.HostKeyCallback == nil {
		t.Error("expected an in-memory host key callback to be set")
	}
}
