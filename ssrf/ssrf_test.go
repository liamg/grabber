package ssrf

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestBlocked(t *testing.T) {
	ips := map[string]net.IP{
		"loopback v4":   net.ParseIP("127.0.0.1"),
		"loopback v6":   net.ParseIP("::1"),
		"private 10":    net.ParseIP("10.0.0.5"),
		"private 172":   net.ParseIP("172.16.3.4"),
		"private 192":   net.ParseIP("192.168.1.1"),
		"link-local":    net.ParseIP("169.254.169.254"), // cloud metadata
		"ula v6":        net.ParseIP("fd00::1"),
		"unspecified":   net.ParseIP("0.0.0.0"),
		"public v4":     net.ParseIP("8.8.8.8"),
		"public v6":     net.ParseIP("2001:4860:4860::8888"),
		"public routed": net.ParseIP("1.1.1.1"),
	}

	tests := []struct {
		level   Level
		blocked []string // keys expected to be blocked; all others allowed
	}{
		{None, nil},
		{Loopback, []string{"loopback v4", "loopback v6", "unspecified"}},
		{Internal, []string{
			"loopback v4", "loopback v6", "private 10", "private 172",
			"private 192", "link-local", "ula v6", "unspecified",
		}},
		{Default, []string{ // resolves to Internal
			"loopback v4", "loopback v6", "private 10", "private 172",
			"private 192", "link-local", "ula v6", "unspecified",
		}},
	}

	for _, tt := range tests {
		t.Run(tt.level.String(), func(t *testing.T) {
			g := New(tt.level, nil)
			want := map[string]bool{}
			for _, k := range tt.blocked {
				want[k] = true
			}
			for name, ip := range ips {
				if got := g.Blocked(ip); got != want[name] {
					t.Errorf("level %v: Blocked(%s=%s) = %v, want %v", tt.level, name, ip, got, want[name])
				}
			}
		})
	}
}

func TestBlocked_NilFailsClosed(t *testing.T) {
	if !New(Internal, nil).Blocked(nil) {
		t.Error("expected a nil IP to be blocked (fail closed)")
	}
	if New(None, nil).Blocked(nil) {
		t.Error("expected None to allow everything, including nil")
	}
}

func TestBlocked_Custom(t *testing.T) {
	// Block only 8.8.8.8.
	g := New(Custom, func(ip net.IP) bool { return ip.Equal(net.ParseIP("8.8.8.8")) })
	if !g.Blocked(net.ParseIP("8.8.8.8")) {
		t.Error("expected custom predicate to block 8.8.8.8")
	}
	if g.Blocked(net.ParseIP("127.0.0.1")) {
		t.Error("custom predicate should allow loopback (only 8.8.8.8 blocked)")
	}

	// Custom with a nil predicate blocks nothing (except nil IP, fail-closed).
	g = New(Custom, nil)
	if g.Blocked(net.ParseIP("127.0.0.1")) {
		t.Error("nil custom predicate should not block")
	}
}

func TestEnabled(t *testing.T) {
	var nilGuard *Guard
	if nilGuard.Enabled() {
		t.Error("nil guard must not be enabled")
	}
	if New(None, nil).Enabled() {
		t.Error("None must not be enabled")
	}
	if !New(Default, nil).Enabled() {
		t.Error("Default (→Internal) must be enabled")
	}
}

func TestCheckHost(t *testing.T) {
	ctx := context.Background()
	g := New(Internal, nil)

	t.Run("blocked IP literal", func(t *testing.T) {
		err := g.CheckHost(ctx, "127.0.0.1")
		var blocked *BlockedAddressError
		if !errors.As(err, &blocked) {
			t.Fatalf("expected BlockedAddressError, got %v", err)
		}
	})

	t.Run("public IP literal allowed", func(t *testing.T) {
		if err := g.CheckHost(ctx, "8.8.8.8"); err != nil {
			t.Errorf("expected public IP to be allowed, got %v", err)
		}
	})

	t.Run("empty host allowed", func(t *testing.T) {
		if err := g.CheckHost(ctx, ""); err != nil {
			t.Errorf("expected empty host to be allowed, got %v", err)
		}
	})

	t.Run("disabled guard allows blocked IP", func(t *testing.T) {
		if err := New(None, nil).CheckHost(ctx, "127.0.0.1"); err != nil {
			t.Errorf("disabled guard should allow anything, got %v", err)
		}
	})

	t.Run("unresolvable host allowed (nothing to block)", func(t *testing.T) {
		if err := g.CheckHost(ctx, "nonexistent.grabber.invalid"); err != nil {
			t.Errorf("expected unresolvable host to pass, got %v", err)
		}
	})

	t.Run("localhost resolves to loopback and is blocked", func(t *testing.T) {
		// "localhost" resolves to 127.0.0.1/::1, both blocked.
		err := g.CheckHost(ctx, "localhost")
		var blocked *BlockedAddressError
		if !errors.As(err, &blocked) {
			t.Fatalf("expected localhost to be blocked, got %v", err)
		}
	})
}

func TestDialContext(t *testing.T) {
	ctx := context.Background()

	t.Run("blocks a loopback dial", func(t *testing.T) {
		dial := New(Internal, nil).DialContext(nil)
		_, err := dial(ctx, "tcp", "127.0.0.1:9")
		var blocked *BlockedAddressError
		if !errors.As(err, &blocked) {
			t.Fatalf("expected BlockedAddressError dialing loopback, got %v", err)
		}
	})

	t.Run("exempt address bypasses the guard", func(t *testing.T) {
		dial := New(Internal, nil).DialContext(ExemptHost("127.0.0.1"))
		// Exempt, so the guard does not reject; the dial itself fails (port 9
		// discard/closed), but not with a BlockedAddressError.
		_, err := dial(ctx, "tcp", "127.0.0.1:9")
		var blocked *BlockedAddressError
		if errors.As(err, &blocked) {
			t.Fatal("exempt address must not be blocked by the guard")
		}
	})

	t.Run("disabled guard does not block", func(t *testing.T) {
		dial := New(None, nil).DialContext(nil)
		_, err := dial(ctx, "tcp", "127.0.0.1:9")
		var blocked *BlockedAddressError
		if errors.As(err, &blocked) {
			t.Fatal("disabled guard must not block")
		}
	})
}

func TestAllowlist(t *testing.T) {
	ctx := context.Background()

	t.Run("allowed IP literal bypasses Blocked", func(t *testing.T) {
		g := New(Internal, nil, "10.0.0.5")
		if g.Blocked(net.ParseIP("10.0.0.5")) {
			t.Error("expected allowlisted IP to be permitted")
		}
		// A different private IP is still blocked.
		if !g.Blocked(net.ParseIP("10.0.0.6")) {
			t.Error("expected non-allowlisted private IP to remain blocked")
		}
	})

	t.Run("allowed CIDR bypasses Blocked", func(t *testing.T) {
		g := New(Internal, nil, "192.168.0.0/16")
		if g.Blocked(net.ParseIP("192.168.5.5")) {
			t.Error("expected IP in allowlisted CIDR to be permitted")
		}
		if !g.Blocked(net.ParseIP("10.0.0.1")) {
			t.Error("expected IP outside the CIDR to remain blocked")
		}
	})

	t.Run("allowed hostname bypasses CheckHost", func(t *testing.T) {
		g := New(Internal, nil, "localhost")
		if err := g.CheckHost(ctx, "localhost"); err != nil {
			t.Errorf("expected allowlisted hostname to pass CheckHost, got %v", err)
		}
		// A non-allowlisted loopback name is still blocked.
		if err := New(Internal, nil).CheckHost(ctx, "localhost"); err == nil {
			t.Error("expected non-allowlisted localhost to be blocked")
		}
	})

	t.Run("allowed IP literal passes CheckHost", func(t *testing.T) {
		g := New(Internal, nil, "127.0.0.1")
		if err := g.CheckHost(ctx, "127.0.0.1"); err != nil {
			t.Errorf("expected allowlisted IP literal to pass CheckHost, got %v", err)
		}
	})

	t.Run("allowed hostname bypasses the dialer", func(t *testing.T) {
		g := New(Internal, nil, "myhost.internal")
		dial := g.DialContext(nil)
		// The host is allowlisted, so the guard does not reject; the dial itself
		// fails (unresolvable), but not with a BlockedAddressError.
		_, err := dial(ctx, "tcp", "myhost.internal:9")
		var blocked *BlockedAddressError
		if errors.As(err, &blocked) {
			t.Fatal("allowlisted host must not be blocked by the dialer")
		}
	})

	t.Run("unparseable entries are ignored", func(t *testing.T) {
		g := New(Internal, nil, "", "not a valid entry with spaces")
		// Still blocks loopback (the junk entries did not open anything up).
		if !g.Blocked(net.ParseIP("127.0.0.1")) {
			t.Error("expected loopback to remain blocked")
		}
	})
}

func TestExemptHost(t *testing.T) {
	if ExemptHost("") != nil {
		t.Error("empty host should return a nil predicate")
	}
	ex := ExemptHost("proxy.example.com")
	if !ex("proxy.example.com:8080") {
		t.Error("expected host:port to match")
	}
	if !ex("PROXY.example.com:8080") {
		t.Error("expected case-insensitive match")
	}
	if ex("other.example.com:8080") {
		t.Error("expected non-matching host to be false")
	}
}

// String gives readable subtest names above.
func (l Level) String() string {
	switch l {
	case Default:
		return "Default"
	case None:
		return "None"
	case Loopback:
		return "Loopback"
	case Internal:
		return "Internal"
	case Custom:
		return "Custom"
	default:
		return "Unknown"
	}
}
