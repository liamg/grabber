package settings

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/liamg/grabber/internal/testcert"
)

func TestMatchClientCertificate(t *testing.T) {
	tests := []struct {
		name     string
		certs    []ClientCertificate
		host     string
		wantCert string // Cert bytes as string; "" means expect nil
	}{
		{
			name:     "host match",
			certs:    []ClientCertificate{{Host: "github.example.com", Cert: []byte("gh")}},
			host:     "github.example.com",
			wantCert: "gh",
		},
		{
			name:     "host case insensitive",
			certs:    []ClientCertificate{{Host: "GitHub.Example.com", Cert: []byte("gh")}},
			host:     "github.example.com",
			wantCert: "gh",
		},
		{
			name:     "host-specific wins over default",
			certs:    []ClientCertificate{{Cert: []byte("default")}, {Host: "github.example.com", Cert: []byte("gh")}},
			host:     "github.example.com",
			wantCert: "gh",
		},
		{
			name:     "falls back to default",
			certs:    []ClientCertificate{{Cert: []byte("default")}, {Host: "github.example.com", Cert: []byte("gh")}},
			host:     "gitlab.example.com",
			wantCert: "default",
		},
		{
			name:     "no match and no default",
			certs:    []ClientCertificate{{Host: "github.example.com", Cert: []byte("gh")}},
			host:     "gitlab.example.com",
			wantCert: "",
		},
		{
			name:     "empty",
			host:     "github.example.com",
			wantCert: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Settings{ClientCertificates: tt.certs}
			got := s.MatchClientCertificate(tt.host)
			if tt.wantCert == "" {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil || string(got.Cert) != tt.wantCert {
				t.Errorf("cert = %+v, want Cert=%q", got, tt.wantCert)
			}
		})
	}
}

func TestMatchProxy(t *testing.T) {
	mustURL := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		return u
	}

	global := ProxyConfig{URL: mustURL("http://global-proxy:8080")}
	registry := ProxyConfig{Host: "registry.terraform.io", URL: mustURL("http://registry-proxy:8080")}

	tests := []struct {
		name    string
		proxies []ProxyConfig
		host    string
		wantURL string // "" means expect nil
	}{
		{"host match preferred over global", []ProxyConfig{global, registry}, "registry.terraform.io", "http://registry-proxy:8080"},
		{"host match case insensitive", []ProxyConfig{registry}, "Registry.Terraform.IO", "http://registry-proxy:8080"},
		{"falls back to global", []ProxyConfig{global, registry}, "github.com", "http://global-proxy:8080"},
		{"no match and no global", []ProxyConfig{registry}, "github.com", ""},
		{"empty", nil, "github.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Settings{Proxies: tt.proxies}
			got := s.MatchProxy(tt.host)
			if tt.wantURL == "" {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil || got.URL.String() != tt.wantURL {
				t.Errorf("proxy = %+v, want URL=%q", got, tt.wantURL)
			}
		})
	}
}

func TestProxyURL(t *testing.T) {
	base, _ := url.Parse("http://proxy.example.com:8080")

	t.Run("nil config", func(t *testing.T) {
		var p *ProxyConfig
		if p.ProxyURL() != nil {
			t.Error("expected nil for nil config")
		}
	})

	t.Run("nil URL", func(t *testing.T) {
		p := &ProxyConfig{}
		if p.ProxyURL() != nil {
			t.Error("expected nil for nil URL")
		}
	})

	t.Run("without credentials", func(t *testing.T) {
		p := &ProxyConfig{URL: base}
		got := p.ProxyURL()
		if got.String() != "http://proxy.example.com:8080" {
			t.Errorf("got %q", got)
		}
		if got.User != nil {
			t.Error("expected no userinfo")
		}
	})

	t.Run("with credentials", func(t *testing.T) {
		p := &ProxyConfig{URL: base, Username: "u", Password: "p"}
		got := p.ProxyURL()
		if got.User == nil {
			t.Fatal("expected userinfo")
		}
		if got.User.Username() != "u" {
			t.Errorf("username = %q", got.User.Username())
		}
		if pw, _ := got.User.Password(); pw != "p" {
			t.Errorf("password = %q", pw)
		}
		// The original URL must not be mutated.
		if base.User != nil {
			t.Error("original URL was mutated")
		}
	})
}

func TestTransportForHost(t *testing.T) {
	ca, err := testcert.NewCA()
	if err != nil {
		t.Fatalf("creating CA: %v", err)
	}
	clientCert, clientKey, err := ca.IssueClient("test-client")
	if err != nil {
		t.Fatalf("issuing client cert: %v", err)
	}

	t.Run("nothing configured returns nil", func(t *testing.T) {
		tr, err := Settings{}.TransportForHost("example.com")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if tr != nil {
			t.Errorf("expected nil transport, got %+v", tr)
		}
	})

	t.Run("CA pool is applied", func(t *testing.T) {
		s := Settings{TLSCACerts: [][]byte{ca.CertPEM()}}
		tr, err := s.TransportForHost("example.com")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if tr == nil || tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
			t.Fatal("expected RootCAs to be set")
		}
	})

	t.Run("invalid CA PEM errors", func(t *testing.T) {
		s := Settings{TLSCACerts: [][]byte{[]byte("not a pem")}}
		if _, err := s.TransportForHost("example.com"); err == nil {
			t.Error("expected error for invalid CA PEM")
		}
	})

	t.Run("client certificate is applied for matching host", func(t *testing.T) {
		s := Settings{ClientCertificates: []ClientCertificate{
			{Host: "example.com", Cert: clientCert, Key: clientKey},
		}}
		tr, err := s.TransportForHost("example.com")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if tr == nil || tr.TLSClientConfig == nil || len(tr.TLSClientConfig.Certificates) != 1 {
			t.Fatal("expected one client certificate")
		}

		// A different host with no default gets no transport at all.
		tr, err = s.TransportForHost("other.com")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if tr != nil {
			t.Errorf("expected nil transport for unmatched host, got %+v", tr)
		}
	})

	t.Run("invalid client certificate errors", func(t *testing.T) {
		s := Settings{ClientCertificates: []ClientCertificate{
			{Cert: []byte("bad"), Key: []byte("bad")},
		}}
		if _, err := s.TransportForHost("example.com"); err == nil {
			t.Error("expected error for invalid client certificate")
		}
	})

	t.Run("proxy is applied", func(t *testing.T) {
		proxyURL, _ := url.Parse("http://proxy.example.com:8080")
		s := Settings{Proxies: []ProxyConfig{{URL: proxyURL, Username: "u", Password: "p"}}}
		tr, err := s.TransportForHost("example.com")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if tr == nil || tr.Proxy == nil {
			t.Fatal("expected Proxy to be set")
		}
		req, _ := http.NewRequest(http.MethodGet, "http://target.example.com/", nil)
		got, err := tr.Proxy(req)
		if err != nil {
			t.Fatalf("proxy func: %v", err)
		}
		if got == nil || got.Host != "proxy.example.com:8080" {
			t.Errorf("proxy = %v", got)
		}
		if got.User == nil || got.User.Username() != "u" {
			t.Errorf("expected proxy credentials, got %v", got.User)
		}
	})

	t.Run("base transport is cloned and preserved, not mutated", func(t *testing.T) {
		var dialed bool
		base := http.DefaultTransport.(*http.Transport).Clone()
		dialer := &net.Dialer{}
		base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = true
			return dialer.DialContext(ctx, network, addr)
		}

		s := Settings{
			HTTPTransport: base,
			TLSCACerts:    [][]byte{ca.CertPEM()},
		}
		tr, err := s.TransportForHost("example.com")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if tr == base {
			t.Error("expected a clone, got the same transport")
		}
		if tr.DialContext == nil {
			t.Fatal("expected the base DialContext to be preserved on the clone")
		}
		// The clone keeps the caller's dialer: exercising it proves composition.
		conn, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:1")
		if conn != nil {
			conn.Close()
		}
		_ = err // dial may fail; we only care the custom dialer ran
		if !dialed {
			t.Error("expected the base DialContext to be invoked via the clone")
		}
		// The caller's transport must not have been mutated.
		if base.TLSClientConfig != nil && base.TLSClientConfig.RootCAs != nil {
			t.Error("base transport was mutated")
		}
	})

	t.Run("base transport alone is still cloned and used", func(t *testing.T) {
		base := http.DefaultTransport.(*http.Transport).Clone()
		s := Settings{HTTPTransport: base}
		tr, err := s.TransportForHost("example.com")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if tr == nil {
			t.Fatal("expected a transport when a base transport is configured")
		}
		if tr == base {
			t.Error("expected a clone, got the same transport")
		}
	})
}
