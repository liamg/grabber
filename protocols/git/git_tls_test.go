package git

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	nethttp "net/http"
	"net/url"
	"testing"

	"github.com/liamg/grabber/internal/testcert"
	"github.com/liamg/grabber/settings"
	"github.com/liamg/grabber/ssrf"
)

// TestGitDownload_SSRFBlocksLoopback proves the pre-fetch SSRF check rejects a
// clone whose host resolves to a blocked address, before any network work.
func TestGitDownload_SSRFBlocksLoopback(t *testing.T) {
	d := &Downloader{repoURL: "https://127.0.0.1:1/org/repo.git"}

	t.Run("blocked by default", func(t *testing.T) {
		_, err := d.Download(context.Background(), t.TempDir(), settings.Settings{})
		var blocked *ssrf.BlockedAddressError
		if !errors.As(err, &blocked) {
			t.Fatalf("expected BlockedAddressError, got %v", err)
		}
	})

	t.Run("allowed when disabled", func(t *testing.T) {
		// With the guard off the clone proceeds and fails on connection, not on
		// the SSRF check.
		_, err := d.Download(context.Background(), t.TempDir(), settings.Settings{SSRFLevel: ssrf.None})
		var blocked *ssrf.BlockedAddressError
		if errors.As(err, &blocked) {
			t.Fatal("did not expect an SSRF block when disabled")
		}
	})
}

// tlsSettings builds settings carrying a CA bundle, a default and a host-scoped
// client certificate, and a proxy with credentials.
func tlsSettings(t *testing.T) (settings.Settings, *x509.Certificate) {
	t.Helper()

	ca, err := testcert.NewCA()
	if err != nil {
		t.Fatalf("creating CA: %v", err)
	}
	defaultCert, defaultKey, err := ca.IssueClient("default")
	if err != nil {
		t.Fatalf("issuing default cert: %v", err)
	}
	ghCert, ghKey, err := ca.IssueClient("github.example.com")
	if err != nil {
		t.Fatalf("issuing host cert: %v", err)
	}

	block, err := x509.ParseCertificate(pemBlock(t, ghCert))
	if err != nil {
		t.Fatalf("parsing host cert: %v", err)
	}

	proxyURL, _ := url.Parse("http://proxy.example.com:8080")
	return settings.Settings{
		TLSCACerts: [][]byte{ca.CertPEM()},
		ClientCertificates: []settings.ClientCertificate{
			{Cert: defaultCert, Key: defaultKey},
			{Host: "github.example.com", Cert: ghCert, Key: ghKey},
		},
		Proxies: []settings.ProxyConfig{
			{URL: proxyURL, Username: "u", Password: "p"},
		},
	}, block
}

func TestHTTPTransportFor(t *testing.T) {
	full, ghCert := tlsSettings(t)

	t.Run("https remote gets CA bundle, host cert, and proxy", func(t *testing.T) {
		tr, err := httpTransportFor("https://github.example.com/org/repo.git", full)
		if err != nil {
			t.Fatalf("httpTransportFor: %v", err)
		}
		if tr == nil {
			t.Fatal("expected a transport for a configured https remote")
		}
		if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
			t.Error("expected the configured CA bundle to be applied")
		}
		if len(tr.TLSClientConfig.Certificates) != 1 {
			t.Fatalf("expected one client certificate, got %d", len(tr.TLSClientConfig.Certificates))
		}
		if !bytes.Equal(tr.TLSClientConfig.Certificates[0].Certificate[0], ghCert.Raw) {
			t.Error("expected the host-scoped client certificate to be selected")
		}
		if tr.Proxy == nil {
			t.Fatal("expected a proxy to be configured")
		}
		proxied, err := tr.Proxy(&nethttp.Request{URL: mustURL(t, "https://github.example.com/org/repo.git")})
		if err != nil {
			t.Fatalf("resolving proxy: %v", err)
		}
		if proxied == nil || proxied.Host != "proxy.example.com:8080" {
			t.Fatalf("proxy = %v, want proxy.example.com:8080", proxied)
		}
		// Proxy credentials travel with the URL, so they are not dropped.
		pass, _ := proxied.User.Password()
		if proxied.User.Username() != "u" || pass != "p" {
			t.Errorf("proxy credentials = %v, want u/p", proxied.User)
		}
	})

	t.Run("unmatched host falls back to the default cert", func(t *testing.T) {
		tr, err := httpTransportFor("https://gitlab.example.com/org/repo.git", full)
		if err != nil {
			t.Fatalf("httpTransportFor: %v", err)
		}
		if tr == nil || tr.TLSClientConfig == nil || len(tr.TLSClientConfig.Certificates) != 1 {
			t.Fatal("expected the default client certificate to be applied")
		}
		if bytes.Equal(tr.TLSClientConfig.Certificates[0].Certificate[0], ghCert.Raw) {
			t.Error("expected the default cert, got the host-scoped one")
		}
	})

	t.Run("ssh URL is a no-op", func(t *testing.T) {
		tr, err := httpTransportFor("ssh://git@github.example.com/org/repo.git", full)
		if err != nil {
			t.Fatalf("httpTransportFor: %v", err)
		}
		if tr != nil {
			t.Error("expected no HTTP transport for an ssh remote")
		}
	})

	t.Run("scp URL is a no-op", func(t *testing.T) {
		tr, err := httpTransportFor("git@github.example.com:org/repo.git", full)
		if err != nil {
			t.Fatalf("httpTransportFor: %v", err)
		}
		if tr != nil {
			t.Error("expected no HTTP transport for an scp remote")
		}
	})

	t.Run("no configuration leaves go-git's default client", func(t *testing.T) {
		// The SSRF guard is off, so nothing needs customising.
		tr, err := httpTransportFor("https://github.example.com/org/repo.git",
			settings.Settings{SSRFLevel: ssrf.None})
		if err != nil {
			t.Fatalf("httpTransportFor: %v", err)
		}
		if tr != nil {
			t.Error("expected no transport when nothing is configured")
		}
	})
}

// pemBlock decodes the first PEM block of a certificate to its DER bytes.
func pemBlock(t *testing.T, certPEM []byte) []byte {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block found in certificate")
	}
	return block.Bytes
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	return u
}
