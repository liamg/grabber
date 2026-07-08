package settings

import (
	"context"
	"net"
	"net/url"
	"testing"
	"time"
)

func TestProbeConnect(t *testing.T) {
	ctx := context.Background()

	// A listener we can actually reach.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	host, port, _ := net.SplitHostPort(ln.Addr().String())

	t.Run("disabled is a no-op", func(t *testing.T) {
		// Timeout 0 → no probe, even for an unreachable port.
		if err := (Settings{}).ProbeConnect(ctx, "127.0.0.1", "1"); err != nil {
			t.Errorf("expected nil when disabled, got %v", err)
		}
	})

	t.Run("reachable host passes", func(t *testing.T) {
		s := Settings{ConnectProbeTimeout: 2 * time.Second}
		if err := s.ProbeConnect(ctx, host, port); err != nil {
			t.Errorf("expected reachable host to pass, got %v", err)
		}
	})

	t.Run("unreachable host fails", func(t *testing.T) {
		s := Settings{ConnectProbeTimeout: 500 * time.Millisecond}
		// Port 1 on loopback is (almost certainly) closed → connection refused.
		if err := s.ProbeConnect(ctx, "127.0.0.1", "1"); err == nil {
			t.Error("expected an error for an unreachable port")
		}
	})

	t.Run("empty host or port is a no-op", func(t *testing.T) {
		s := Settings{ConnectProbeTimeout: time.Second}
		if err := s.ProbeConnect(ctx, "", "443"); err != nil {
			t.Errorf("empty host: %v", err)
		}
		if err := s.ProbeConnect(ctx, "example.com", ""); err != nil {
			t.Errorf("empty port: %v", err)
		}
	})

	t.Run("skipped when a proxy is configured for the host", func(t *testing.T) {
		proxyURL, _ := url.Parse("http://proxy.example.com:8080")
		s := Settings{
			ConnectProbeTimeout: 500 * time.Millisecond,
			Proxies:             []ProxyConfig{{URL: proxyURL}},
		}
		// Unreachable port, but the global proxy means we don't probe directly.
		if err := s.ProbeConnect(ctx, "127.0.0.1", "1"); err != nil {
			t.Errorf("expected probe to be skipped under a proxy, got %v", err)
		}
	})
}
