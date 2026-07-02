package git

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"

	cryptossh "golang.org/x/crypto/ssh"

	"github.com/liamg/grabber/settings"
)

// newTestHostKey returns a fresh SSH public key and its known_hosts
// authorized-key encoding (e.g. "ssh-ed25519 AAAA...\n").
func newTestHostKey(t *testing.T) (cryptossh.PublicKey, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	signer, err := cryptossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	pub := signer.PublicKey()
	return pub, cryptossh.MarshalAuthorizedKey(pub)
}

func knownHostsLine(host string, authKey []byte) []byte {
	return append([]byte(host+" "), authKey...)
}

func TestKnownHostsCallback(t *testing.T) {
	keyA, authA := newTestHostKey(t)
	keyB, _ := newTestHostKey(t)

	addr := &net.TCPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 22}

	t.Run("matching key is accepted", func(t *testing.T) {
		cb, err := knownHostsCallback(knownHostsLine("example.com", authA))
		if err != nil {
			t.Fatalf("building callback: %v", err)
		}
		if err := cb("example.com:22", addr, keyA); err != nil {
			t.Errorf("expected match to be accepted, got %v", err)
		}
	})

	t.Run("changed key is rejected", func(t *testing.T) {
		cb, _ := knownHostsCallback(knownHostsLine("example.com", authA))
		if err := cb("example.com:22", addr, keyB); err == nil {
			t.Error("expected changed key to be rejected")
		}
	})

	t.Run("unknown host is allowed", func(t *testing.T) {
		cb, _ := knownHostsCallback(knownHostsLine("example.com", authA))
		if err := cb("other.com:22", addr, keyB); err != nil {
			t.Errorf("expected unknown host to be allowed, got %v", err)
		}
	})

	t.Run("non-standard port via bracket notation", func(t *testing.T) {
		cb, _ := knownHostsCallback(knownHostsLine("[example.com]:2222", authA))
		if err := cb("example.com:2222", addr, keyA); err != nil {
			t.Errorf("expected bracketed-port match, got %v", err)
		}
	})

	t.Run("multiple keys for a host", func(t *testing.T) {
		data := append(knownHostsLine("example.com", authA), knownHostsLine("example.com", cryptossh.MarshalAuthorizedKey(keyB))...)
		cb, err := knownHostsCallback(data)
		if err != nil {
			t.Fatalf("building callback: %v", err)
		}
		if err := cb("example.com:22", addr, keyA); err != nil {
			t.Errorf("keyA should match: %v", err)
		}
		if err := cb("example.com:22", addr, keyB); err != nil {
			t.Errorf("keyB should match: %v", err)
		}
	})

	t.Run("comma-separated host,ip patterns", func(t *testing.T) {
		cb, err := knownHostsCallback(knownHostsLine("example.com,1.2.3.4", authA))
		if err != nil {
			t.Fatalf("building callback: %v", err)
		}
		if err := cb("example.com:22", addr, keyA); err != nil {
			t.Errorf("expected match on first pattern, got %v", err)
		}
		if err := cb("1.2.3.4:22", addr, keyA); err != nil {
			t.Errorf("expected match on ip pattern, got %v", err)
		}
	})

	t.Run("hashed entries are ignored (host treated as unknown)", func(t *testing.T) {
		hashed := append([]byte("|1|abcd=|efgh= "), authA...)
		cb, err := knownHostsCallback(hashed)
		if err != nil {
			t.Fatalf("building callback: %v", err)
		}
		// Any host is unknown because the only entry was hashed and skipped.
		if err := cb("example.com:22", addr, keyB); err != nil {
			t.Errorf("expected unknown (hashed skipped) to be allowed, got %v", err)
		}
	})

	t.Run("malformed known_hosts errors", func(t *testing.T) {
		if _, err := knownHostsCallback([]byte("example.com not-a-valid-key")); err == nil {
			t.Error("expected error for malformed known_hosts")
		}
	})
}

func TestNormalizeKnownHost(t *testing.T) {
	cases := map[string]string{
		"example.com":        "example.com",
		"example.com:22":     "example.com",
		"EXAMPLE.com:22":     "example.com",
		"[example.com]:2222": "example.com",
		"  example.com  ":    "example.com",
		"1.2.3.4:22":         "1.2.3.4",
	}
	for in, want := range cases {
		if got := normalizeKnownHost(in); got != want {
			t.Errorf("normalizeKnownHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSSHHostKeyCallback_Selection(t *testing.T) {
	_, authA := newTestHostKey(t)
	otherKey, _ := newTestHostKey(t)
	addr := &net.TCPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 22}

	t.Run("insecure skip accepts any key", func(t *testing.T) {
		cb, err := sshHostKeyCallback(settings.Settings{
			Git: settings.GitConfig{InsecureSkipHostKeyVerify: true},
		}, "example.com")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if cb == nil {
			t.Fatal("expected a non-nil callback")
		}
		if err := cb("example.com:22", addr, otherKey); err != nil {
			t.Errorf("insecure callback should accept any key, got %v", err)
		}
	})

	t.Run("known hosts enforced when configured", func(t *testing.T) {
		cb, err := sshHostKeyCallback(settings.Settings{
			Git: settings.GitConfig{KnownHosts: knownHostsLine("example.com", authA)},
		}, "example.com")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if cb == nil {
			t.Fatal("expected a non-nil callback")
		}
		if err := cb("example.com:22", addr, otherKey); err == nil {
			t.Error("expected mismatch rejection from configured known_hosts")
		}
	})

	t.Run("system fallback on and no known hosts uses default (nil)", func(t *testing.T) {
		cb, err := sshHostKeyCallback(settings.Settings{}, "example.com")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if cb != nil {
			t.Error("expected nil callback so go-git uses its ~/.ssh/known_hosts default")
		}
	})

	t.Run("no system fallback and no known hosts accepts any key", func(t *testing.T) {
		cb, err := sshHostKeyCallback(settings.Settings{NoSystemFallback: true}, "example.com")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if cb == nil {
			t.Fatal("expected a non-nil accept-all callback")
		}
		if err := cb("example.com:22", addr, otherKey); err != nil {
			t.Errorf("expected accept-all, got %v", err)
		}
	})
}
