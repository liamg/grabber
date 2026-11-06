package settings

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liamg/grabber/ssrf"
)

// newProxy starts a loopback HTTP proxy that answers every request, and counts
// how many it saw.
func newProxy(t *testing.T) (*url.URL, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "via proxy")
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u, &hits
}

func get(t *testing.T, rt http.RoundTripper, target string) (string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

// envSettings returns settings whose base transport takes its proxy from a
// function, standing in for HTTPS_PROXY / NO_PROXY.
func envSettings(proxy func(*http.Request) (*url.URL, error)) Settings {
	return Settings{HTTPTransport: &http.Transport{Proxy: proxy}, SSRFAllow: []string{"registry.example.invalid"}}
}

func TestRoundTripperForHost_EnvProxyOnPrivateAddress(t *testing.T) {
	proxyURL, hits := newProxy(t) // listens on 127.0.0.1, blocked by the guard

	t.Run("request routed through the proxy reaches it", func(t *testing.T) {
		s := envSettings(http.ProxyURL(proxyURL))
		rt, err := s.RoundTripperForHost("registry.example.invalid")
		if err != nil {
			t.Fatal(err)
		}
		body, err := get(t, rt, "http://registry.example.invalid/x")
		if err != nil {
			t.Fatalf("proxied request failed: %v", err)
		}
		if body != "via proxy" {
			t.Fatalf("body = %q", body)
		}
		if hits.Load() == 0 {
			t.Fatal("proxy was not used")
		}
	})

	t.Run("target on a private address is refused before the proxy", func(t *testing.T) {
		before := hits.Load()
		s := envSettings(http.ProxyURL(proxyURL))
		rt, _ := s.RoundTripperForHost("10.0.0.1")
		_, err := get(t, rt, "http://10.0.0.1/x")
		var blocked *ssrf.BlockedAddressError
		if !errors.As(err, &blocked) {
			t.Fatalf("expected a blocked-address error, got %v", err)
		}
		if hits.Load() != before {
			t.Fatal("the request must not reach the proxy")
		}
	})

	t.Run("non-canonical literal is refused before the proxy", func(t *testing.T) {
		before := hits.Load()
		s := envSettings(http.ProxyURL(proxyURL))
		rt, _ := s.RoundTripperForHost("2852039166")
		_, err := get(t, rt, "http://2852039166/x")
		if err == nil || !strings.Contains(err.Error(), "non-canonical") {
			t.Fatalf("expected a non-canonical literal error, got %v", err)
		}
		if hits.Load() != before {
			t.Fatal("the request must not reach the proxy")
		}
	})

	t.Run("a direct dial to the proxy address stays blocked", func(t *testing.T) {
		// NO_PROXY case: the proxy function declines, so the request dials the
		// target itself. Pointing it at the proxy's own address must not get the
		// proxy exemption.
		before := hits.Load()
		s := envSettings(func(*http.Request) (*url.URL, error) { return nil, nil })
		rt, _ := s.RoundTripperForHost(proxyURL.Hostname())
		_, err := get(t, rt, proxyURL.String())
		var blocked *ssrf.BlockedAddressError
		if !errors.As(err, &blocked) {
			t.Fatalf("expected a blocked-address error, got %v", err)
		}
		if hits.Load() != before {
			t.Fatal("the request must not be served")
		}
	})

	t.Run("exemption never leaks between concurrent requests", func(t *testing.T) {
		// One transport, proxied and direct requests in flight together. The
		// direct ones dial the proxy's own address and must all be blocked.
		s := envSettings(func(r *http.Request) (*url.URL, error) {
			if r.URL.Host == proxyURL.Host {
				return nil, nil
			}
			return proxyURL, nil
		})
		rt, _ := s.RoundTripperForHost("registry.example.invalid")
		var wg sync.WaitGroup
		var proxiedOK, directBlocked atomic.Int32
		for i := 0; i < 100; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				if _, err := get(t, rt, "http://registry.example.invalid/x"); err == nil {
					proxiedOK.Add(1)
				}
			}()
			go func() {
				defer wg.Done()
				var blocked *ssrf.BlockedAddressError
				if _, err := get(t, rt, proxyURL.String()); errors.As(err, &blocked) {
					directBlocked.Add(1)
				}
			}()
		}
		wg.Wait()
		if proxiedOK.Load() != 100 || directBlocked.Load() != 100 {
			t.Fatalf("proxied ok = %d, direct blocked = %d, want 100 each", proxiedOK.Load(), directBlocked.Load())
		}
	})

	t.Run("redirect to an internal address is refused before the proxy", func(t *testing.T) {
		var seen atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen.Add(1)
			http.Redirect(w, r, "http://10.0.0.1/y", http.StatusFound)
		}))
		t.Cleanup(srv.Close)
		redirProxy, _ := url.Parse(srv.URL)
		s := envSettings(http.ProxyURL(redirProxy))
		rt, _ := s.RoundTripperForHost("registry.example.invalid")
		_, err := get(t, rt, "http://registry.example.invalid/x")
		var blocked *ssrf.BlockedAddressError
		if !errors.As(err, &blocked) {
			t.Fatalf("expected the redirect target to be refused, got %v", err)
		}
		if seen.Load() != 1 {
			t.Fatalf("the proxy saw %d requests, want only the first", seen.Load())
		}
	})

	t.Run("a Host header cannot name an internal target", func(t *testing.T) {
		for _, host := range []string{"169.254.169.254", "169.254.169.254:80", "::ffff:a00:1", "[::ffff:a00:1]:80"} {
			before := hits.Load()
			s := envSettings(http.ProxyURL(proxyURL))
			rt, _ := s.RoundTripperForHost("registry.example.invalid")
			req, _ := http.NewRequest(http.MethodGet, "http://registry.example.invalid/x", nil)
			req.Host = host
			_, err := rt.RoundTrip(req)
			var blocked *ssrf.BlockedAddressError
			if !errors.As(err, &blocked) {
				t.Fatalf("Host %q: expected a blocked-address error, got %v", host, err)
			}
			if hits.Load() != before {
				t.Fatalf("Host %q: the request must not reach the proxy", host)
			}
		}
	})
}

func TestRoundTripperForHost_HTTPSThroughProxy(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "via tunnel")
	}))
	t.Cleanup(target.Close)

	var connectHost atomic.Value
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusBadRequest)
			return
		}
		connectHost.Store(r.Host)
		up, err := net.Dial("tcp", target.Listener.Addr().String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		go func() { _, _ = io.Copy(up, conn); _ = up.Close() }()
		go func() { _, _ = io.Copy(conn, up); _ = conn.Close() }()
	}))
	t.Cleanup(proxy.Close)
	proxyURL, _ := url.Parse(proxy.URL) // loopback: blocked by the guard unless exempt

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: target.Certificate().Raw})
	// example.com is allowlisted by name so the test does no DNS.
	s := Settings{HTTPTransport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, TLSCACerts: [][]byte{caPEM}, SSRFAllow: []string{"example.com"}}
	rt, err := s.RoundTripperForHost("example.com")
	if err != nil {
		t.Fatal(err)
	}

	body, err := get(t, rt, "https://example.com/x") // the certificate httptest issues is valid for example.com
	if err != nil {
		t.Fatalf("HTTPS through the proxy failed: %v", err)
	}
	if body != "via tunnel" {
		t.Fatalf("body = %q", body)
	}
	if got, _ := connectHost.Load().(string); got != "example.com:443" {
		t.Fatalf("the proxy was asked to CONNECT to %q", got)
	}

	// An internal HTTPS target is refused before any CONNECT.
	connectHost.Store("")
	_, err = get(t, rt, "https://10.0.0.1/x")
	var blocked *ssrf.BlockedAddressError
	if !errors.As(err, &blocked) {
		t.Fatalf("expected a blocked-address error, got %v", err)
	}
	if got, _ := connectHost.Load().(string); got != "" {
		t.Fatalf("the proxy must not be asked to CONNECT, got %q", got)
	}
}

func TestEnvProxyApplies_NoProxyWithPort(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.example:3128")
	// Stands in for NO_PROXY=git.example:8443.
	s := envSettings(func(r *http.Request) (*url.URL, error) {
		if r.URL.Host == "git.example:8443" {
			return nil, nil
		}
		return proxyURL, nil
	})
	if s.envProxyApplies("https", "git.example", "8443") {
		t.Error("a NO_PROXY entry with a port must apply when the port is given")
	}
	if !s.envProxyApplies("https", "git.example", "443") {
		t.Error("another port is proxied")
	}
}

func TestRoundTripperForHost_NoEnvProxy(t *testing.T) {
	t.Run("nothing configured and guard off gives nil", func(t *testing.T) {
		rt, err := Settings{SSRFLevel: ssrf.None}.RoundTripperForHost("example.com")
		if err != nil || rt != nil {
			t.Fatalf("got (%v, %v), want (nil, nil)", rt, err)
		}
	})

	t.Run("without a proxy the guard still blocks loopback", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		t.Cleanup(srv.Close)
		rt, err := Settings{HTTPTransport: &http.Transport{}}.RoundTripperForHost("127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		_, err = get(t, rt, srv.URL)
		var blocked *ssrf.BlockedAddressError
		if !errors.As(err, &blocked) {
			t.Fatalf("expected a blocked-address error, got %v", err)
		}
	})

	t.Run("a proxy set through grabber's own options is not wrapped", func(t *testing.T) {
		proxyURL, _ := newProxy(t)
		s := Settings{Proxies: []ProxyConfig{{URL: proxyURL}}}
		rt, err := s.RoundTripperForHost("example.invalid")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := rt.(*http.Transport); !ok {
			t.Fatalf("expected the plain transport, got %T", rt)
		}
	})
}

func TestEnvProxyApplies(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.example:3128")
	via := func(host string) func(*http.Request) (*url.URL, error) {
		return func(r *http.Request) (*url.URL, error) {
			if r.URL.Hostname() == host || r.URL.Hostname() == "2001:db8::2" {
				return nil, nil
			}
			return proxyURL, nil
		}
	}
	s := envSettings(via("code.example"))
	for _, tt := range []struct {
		scheme, host, port string
		want               bool
	}{
		{"https", "github.com", "", true},
		{"https", "github.com", "443", true},
		{"https", "code.example", "", false}, // NO_PROXY
		{"ssh", "github.com", "", false},     // the proxy is HTTP only
		{"", "github.com", "", false},
		{"https", "2001:db8::1", "", true}, // a bare IPv6 literal is bracketed, not split at a colon
		{"https", "2001:db8::1", "8443", true},
		{"https", "2001:db8::2", "", false}, // NO_PROXY names this literal; a split host would miss it
	} {
		if got := s.envProxyApplies(tt.scheme, tt.host, tt.port); got != tt.want {
			t.Errorf("envProxyApplies(%q, %q, %q) = %v, want %v", tt.scheme, tt.host, tt.port, got, tt.want)
		}
	}
	if envSettings(nil).envProxyApplies("https", "github.com", "") {
		t.Error("no proxy function must mean no env proxy")
	}
}

func TestCheckSSRFHostFor(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.example:3128")

	t.Run("name only the proxy can resolve is allowed behind an env proxy", func(t *testing.T) {
		s := envSettings(http.ProxyURL(proxyURL))
		if err := s.CheckSSRFHostFor(context.Background(), "https", "unresolvable-name.invalid", "443"); err != nil {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("the same name is refused without an env proxy (fails closed)", func(t *testing.T) {
		s := envSettings(nil)
		if err := s.CheckSSRFHostFor(context.Background(), "https", "unresolvable-name.invalid", "443"); err == nil {
			t.Fatal("expected the fail-closed refusal")
		}
	})

	t.Run("private literal is refused either way", func(t *testing.T) {
		s := envSettings(http.ProxyURL(proxyURL))
		if err := s.CheckSSRFHostFor(context.Background(), "https", "10.0.0.1", "443"); err == nil {
			t.Fatal("expected a refusal")
		}
	})

	t.Run("ssh remote keeps the fail-closed check", func(t *testing.T) {
		s := envSettings(http.ProxyURL(proxyURL))
		if err := s.CheckSSRFHostFor(context.Background(), "", "unresolvable-name.invalid", "22"); err == nil {
			t.Fatal("expected the fail-closed refusal")
		}
	})
}

func TestProbeConnectFor_SkipsEnvProxiedHosts(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.example:3128")
	s := envSettings(http.ProxyURL(proxyURL))
	s.ConnectProbeTimeout = time.Second
	// 192.0.2.1 is TEST-NET-1: a direct probe would be blocked or time out.
	if err := s.ProbeConnectFor(context.Background(), "https", "192.0.2.1", "443"); err != nil {
		t.Fatalf("probe must be skipped for an env-proxied host, got %v", err)
	}
	if err := s.ProbeConnectFor(context.Background(), "ssh", "127.0.0.1", "22"); err == nil {
		t.Fatal("an ssh probe is direct and must still be guarded")
	}
}

// The decision to skip the direct probe and use the proxy-aware check must come
// from the same transport that TransportForHost builds on, not from the process
// environment: an embedding application may have replaced http.DefaultTransport.
func TestEnvProxyApplies_UsesTheBaseTransportsProxy(t *testing.T) {
	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })
	proxyURL, _ := url.Parse("http://proxy.example:3128")

	http.DefaultTransport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	if !(Settings{}).envProxyApplies("https", "github.com", "") {
		t.Error("a default transport with a proxy must count")
	}

	http.DefaultTransport = &http.Transport{} // replaced by one with no proxy
	if (Settings{}).envProxyApplies("https", "github.com", "") {
		t.Error("a default transport with no proxy must not count, whatever the environment says")
	}

	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	if (Settings{}).envProxyApplies("https", "github.com", "") {
		t.Error("a default transport that is not an *http.Transport has no proxy to read")
	}

	// A transport supplied to WithHTTPTransport wins over the default.
	http.DefaultTransport = &http.Transport{}
	own := Settings{HTTPTransport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	if !own.envProxyApplies("https", "github.com", "") {
		t.Error("the transport passed to WithHTTPTransport must be the base")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
