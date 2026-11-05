package ssrf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/idna"
)

func stubLookup(t *testing.T, fn func(ctx context.Context, host string) ([]net.IPAddr, error)) {
	t.Helper()
	orig := lookupIPAddr
	lookupIPAddr = fn
	sharedLookups.reset()
	t.Cleanup(func() { lookupIPAddr = orig; sharedLookups.reset() })
}

func TestCheckProxiedHost(t *testing.T) {
	resolveTo := func(ips ...string) func(context.Context, string) ([]net.IPAddr, error) {
		return func(context.Context, string) ([]net.IPAddr, error) {
			var out []net.IPAddr
			for _, s := range ips {
				out = append(out, net.IPAddr{IP: net.ParseIP(s)})
			}
			return out, nil
		}
	}
	fails := func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("no such host") }

	tests := []struct {
		name    string
		host    string
		lookup  func(context.Context, string) ([]net.IPAddr, error)
		allow   []string
		wantErr bool
	}{
		{name: "public IP literal", host: "8.8.8.8"},
		{name: "private IP literal", host: "10.0.0.1", wantErr: true},
		{name: "loopback literal", host: "127.0.0.1", wantErr: true},
		{name: "metadata literal", host: "169.254.169.254", wantErr: true},
		{name: "IPv6 loopback", host: "::1", wantErr: true},
		{name: "NAT64 of private", host: "64:ff9b::a00:1", wantErr: true},
		{name: "6to4 of private", host: "2002:a00:1::", wantErr: true},
		{name: "IPv4-compatible of private", host: "::a00:1", wantErr: true},
		{name: "NAT64 of public", host: "64:ff9b::808:808"},
		{name: "local-use NAT64 prefix", host: "64:ff9b:1::a00:1", wantErr: true},
		{name: "Teredo with private server", host: "2001:0:a00:1::1", wantErr: true},
		{name: "Teredo with private client", host: "2001:0:808:808::f5ff:fffe", wantErr: true},
		{name: "Teredo with public server and client", host: "2001:0:808:808::f7f7:f7f7"},
		{name: "decimal literal", host: "2852039166", wantErr: true},
		{name: "short literal", host: "127.1", wantErr: true},
		{name: "hex literal", host: "0x7f.0.0.1", wantErr: true},
		{name: "octal literal", host: "0177.0.0.1", wantErr: true},
		{name: "bare 0x", host: "0x", wantErr: true},
		{name: "trailing dot on private", host: "10.0.0.1.", wantErr: true},
		{name: "two trailing dots", host: "10.0.0.1..", wantErr: true},
		{name: "empty label", host: "10.0.0..1", wantErr: true},
		{name: "leading dot", host: ".example.com", wantErr: true},
		{name: "fullwidth digits", host: "１２７.０.０.１", wantErr: true},
		{name: "ideographic full stop", host: "127。0。0。1", wantErr: true},
		{name: "name resolving to private", host: "internal.example", lookup: resolveTo("10.1.2.3"), wantErr: true},
		{name: "name with one private among public", host: "mixed.example", lookup: resolveTo("8.8.8.8", "10.1.2.3"), wantErr: true},
		{name: "name resolving to public", host: "github.com", lookup: resolveTo("140.82.112.3")},
		{name: "name the runner cannot resolve", host: "github.com", lookup: fails},
		{name: "allowed host skips the check", host: "code.example", lookup: resolveTo("10.1.2.3"), allow: []string{"code.example"}},
		{name: "allowed range skips the check", host: "code.example", lookup: resolveTo("10.1.2.3"), allow: []string{"10.1.2.0/24"}},
		{name: "host off the allowlist still refused", host: "other.example", lookup: resolveTo("10.9.9.9"), allow: []string{"code.example"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.lookup != nil {
				stubLookup(t, tt.lookup)
			} else {
				stubLookup(t, fails)
			}
			err := New(Default, nil, tt.allow...).CheckProxiedHost(context.Background(), tt.host)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckProxiedHost(%q) error = %v, wantErr %v", tt.host, err, tt.wantErr)
			}
		})
	}
}

func TestCheckProxiedHost_DisabledGuard(t *testing.T) {
	if err := New(None, nil).CheckProxiedHost(context.Background(), "10.0.0.1"); err != nil {
		t.Fatalf("disabled guard must allow everything, got %v", err)
	}
}

func TestCheckProxiedHost_LookupIsBounded(t *testing.T) {
	orig := proxiedLookupTimeout
	proxiedLookupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { proxiedLookupTimeout = orig })
	var deadline time.Time
	stubLookup(t, func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		deadline, _ = ctx.Deadline()
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second): // fail the test rather than hang it
		}
		return nil, ctx.Err()
	})

	start := time.Now()
	if err := New(Default, nil).CheckProxiedHost(context.Background(), "slow.example"); err != nil {
		t.Fatalf("a lookup that times out must be allowed, got %v", err)
	}
	if deadline.IsZero() || deadline.Sub(start) > proxiedLookupTimeout+time.Second {
		t.Fatalf("the lookup context must carry the timeout, got deadline %v", deadline)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("lookup was not bounded: took %s", d)
	}
}

func TestIsNumericHost_BareHex(t *testing.T) {
	if !isNumericHost("0x") {
		t.Fatal("a bare 0x must count as a numeric host")
	}
}

func TestLookupCache(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	stubLookup(t, func(ctx context.Context, host string) ([]net.IPAddr, error) {
		mu.Lock()
		calls[host]++
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // let concurrent callers overlap
		if err := ctx.Err(); err != nil {
			return nil, err // a lookup run on a caller's cancelled context fails
		}
		if host == "down.example" {
			return nil, errors.New("no such host")
		}
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	})
	now := time.Unix(1000, 0)
	var nowMu sync.Mutex
	c := &lookupCache{now: func() time.Time { nowMu.Lock(); defer nowMu.Unlock(); return now }}
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.lookup(ctx, "ok.example", time.Second)
		}()
	}
	wg.Wait()
	if calls["ok.example"] != 1 {
		t.Fatalf("50 concurrent lookups ran %d times, want 1", calls["ok.example"])
	}

	_, _ = c.lookup(ctx, "ok.example", time.Second)
	if _, err := c.lookup(ctx, "down.example", time.Second); err == nil {
		t.Fatal("expected the failure")
	}
	if _, err := c.lookup(ctx, "down.example", time.Second); err == nil {
		t.Fatal("a failure must be served from the cache as a failure")
	}
	if calls["ok.example"] != 1 || calls["down.example"] != 1 {
		t.Fatalf("calls = %v, want one lookup per name", calls)
	}

	nowMu.Lock()
	now = now.Add(lookupCacheTTL + time.Second)
	nowMu.Unlock()
	_, _ = c.lookup(ctx, "ok.example", time.Second)
	if calls["ok.example"] != 2 {
		t.Fatalf("an expired entry must be looked up again, calls = %v", calls)
	}

	// A caller that gives up returns at once and does not poison the others.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.lookup(cancelled, "slow.example", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if addrs, err := c.lookup(ctx, "slow.example", time.Second); err != nil || len(addrs) != 1 {
		t.Fatalf("a later caller must still get the answer, got %v, %v", addrs, err)
	}
}

func TestLookupCache_IsBounded(t *testing.T) {
	stubLookup(t, func(context.Context, string) ([]net.IPAddr, error) { return nil, nil })
	c := &lookupCache{now: time.Now}
	for i := 0; i < lookupCacheMax*2; i++ {
		_, _ = c.lookup(context.Background(), fmt.Sprintf("h%d.example", i), time.Second)
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > lookupCacheMax {
		t.Fatalf("cache holds %d entries, limit %d", n, lookupCacheMax)
	}
}

func TestCheckProxiedHost_AllowlistOverridesEmbeddedChecks(t *testing.T) {
	stubLookup(t, func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("no such host") })
	ctx := context.Background()
	if err := New(Default, nil).CheckProxiedHost(ctx, "2002:a00:1::1"); err == nil {
		t.Fatal("6to4 of a private address must be refused")
	}
	if err := New(Default, nil, "2002::/16").CheckProxiedHost(ctx, "2002:a00:1::1"); err != nil {
		t.Fatalf("an allowlisted range must bypass the guard, got %v", err)
	}
	if err := New(Loopback, nil).CheckProxiedHost(ctx, "64:ff9b:1::808:808"); err != nil {
		t.Fatalf("the local-use NAT64 refusal belongs to the Internal level only, got %v", err)
	}
}

// For a plain http:// request net/http builds the absolute URI with an IDNA
// profile that does no mapping, so the proxy can receive a different spelling
// than the https:// CONNECT line carries. Both are checked.
func TestCheckProxiedHost_BothIDNAForms(t *testing.T) {
	const host = "ｅｖｉｌ.example.com" // fullwidth letters
	lookupForm, err := idna.Lookup.ToASCII(host)
	if err != nil {
		t.Fatal(err)
	}
	punycodeForm, err := idna.ToASCII(host)
	if err != nil {
		t.Fatal(err)
	}
	if lookupForm == punycodeForm {
		t.Fatal("the test needs a host whose two forms differ")
	}

	stubLookup(t, func(_ context.Context, name string) ([]net.IPAddr, error) {
		switch name {
		case lookupForm:
			return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil // public
		case punycodeForm:
			return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil // metadata
		}
		return nil, errors.New("no such host")
	})

	var bae *BlockedAddressError
	if err := New(Default, nil).CheckProxiedHost(context.Background(), host); !errors.As(err, &bae) {
		t.Fatalf("expected the punycode form to be refused, got %v", err)
	}
}
