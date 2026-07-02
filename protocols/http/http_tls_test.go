package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/liamg/grabber/internal/testcert"
	"github.com/liamg/grabber/settings"
)

// newCAServer starts an HTTPS server whose certificate is signed by a fresh
// test CA. If clientCA is true the server also requires a client certificate
// signed by the same CA (mutual TLS).
func newCAServer(t *testing.T, ca *testcert.CA, clientCA bool, handler http.Handler) *httptest.Server {
	t.Helper()

	certPEM, keyPEM, err := ca.IssueServer("127.0.0.1")
	if err != nil {
		t.Fatalf("issuing server cert: %v", err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("loading server cert: %v", err)
	}

	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if clientCA {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(ca.CertPEM())
		srv.TLS.ClientCAs = pool
		srv.TLS.ClientAuth = tls.RequireAndVerifyClientCert
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func contentHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
}

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(data) != expected {
		t.Errorf("%s: got %q, want %q", path, string(data), expected)
	}
}

func TestDownload_CustomCA(t *testing.T) {
	ca, err := testcert.NewCA()
	if err != nil {
		t.Fatalf("creating CA: %v", err)
	}
	srv := newCAServer(t, ca, false, contentHandler("secure content"))

	t.Run("fails without the CA", func(t *testing.T) {
		d := &Downloader{url: srv.URL + "/file.txt"}
		if _, err := d.Download(context.Background(), t.TempDir(), settings.Settings{}); err == nil {
			t.Fatal("expected TLS verification failure without the custom CA")
		}
	})

	t.Run("succeeds with the CA", func(t *testing.T) {
		s := settings.Settings{TLSCACerts: [][]byte{ca.CertPEM()}}
		dst := t.TempDir()
		d := &Downloader{url: srv.URL + "/file.txt"}
		if _, err := d.Download(context.Background(), dst, s); err != nil {
			t.Fatalf("download with custom CA: %v", err)
		}
		assertFileContent(t, filepath.Join(dst, "file.txt"), "secure content")
	})
}

func TestDownload_MutualTLS(t *testing.T) {
	ca, err := testcert.NewCA()
	if err != nil {
		t.Fatalf("creating CA: %v", err)
	}
	srv := newCAServer(t, ca, true, contentHandler("mtls content"))

	clientCert, clientKey, err := ca.IssueClient("grabber-client")
	if err != nil {
		t.Fatalf("issuing client cert: %v", err)
	}

	t.Run("fails without a client certificate", func(t *testing.T) {
		s := settings.Settings{TLSCACerts: [][]byte{ca.CertPEM()}}
		d := &Downloader{url: srv.URL + "/file.txt"}
		if _, err := d.Download(context.Background(), t.TempDir(), s); err == nil {
			t.Fatal("expected failure without a client certificate")
		}
	})

	t.Run("succeeds with the default client certificate", func(t *testing.T) {
		s := settings.Settings{
			TLSCACerts:         [][]byte{ca.CertPEM()},
			ClientCertificates: []settings.ClientCertificate{{Cert: clientCert, Key: clientKey}},
		}
		dst := t.TempDir()
		d := &Downloader{url: srv.URL + "/file.txt"}
		if _, err := d.Download(context.Background(), dst, s); err != nil {
			t.Fatalf("download with client cert: %v", err)
		}
		assertFileContent(t, filepath.Join(dst, "file.txt"), "mtls content")
	})

	t.Run("succeeds with a host-scoped client certificate", func(t *testing.T) {
		s := settings.Settings{
			TLSCACerts: [][]byte{ca.CertPEM()},
			ClientCertificates: []settings.ClientCertificate{
				{Host: "127.0.0.1", Cert: clientCert, Key: clientKey},
			},
		}
		dst := t.TempDir()
		d := &Downloader{url: srv.URL + "/file.txt"}
		if _, err := d.Download(context.Background(), dst, s); err != nil {
			t.Fatalf("download with host-scoped client cert: %v", err)
		}
		assertFileContent(t, filepath.Join(dst, "file.txt"), "mtls content")
	})

	t.Run("host-scoped certificate for another host is not used", func(t *testing.T) {
		s := settings.Settings{
			TLSCACerts: [][]byte{ca.CertPEM()},
			ClientCertificates: []settings.ClientCertificate{
				{Host: "other.example.com", Cert: clientCert, Key: clientKey},
			},
		}
		d := &Downloader{url: srv.URL + "/file.txt"}
		if _, err := d.Download(context.Background(), t.TempDir(), s); err == nil {
			t.Fatal("expected failure: the client certificate is scoped to a different host")
		}
	})
}

// TestDownload_Proxy verifies plain-HTTP requests are routed through the
// configured proxy. The target host does not resolve, so success proves the
// request went via the proxy.
func TestDownload_Proxy(t *testing.T) {
	proxied := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A plain-HTTP proxy request carries the absolute target URL.
		if r.URL.Scheme != "http" || r.URL.Host != "unresolvable.grabber.invalid" {
			http.Error(w, "unexpected proxy target: "+r.URL.String(), http.StatusBadGateway)
			return
		}
		proxied++
		_, _ = w.Write([]byte("via proxy"))
	}))
	defer proxy.Close()

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatalf("parsing proxy url: %v", err)
	}

	t.Run("global proxy is used", func(t *testing.T) {
		proxied = 0
		s := settings.Settings{Proxies: []settings.ProxyConfig{{URL: proxyURL}}}
		dst := t.TempDir()
		d := &Downloader{url: "http://unresolvable.grabber.invalid/file.txt"}
		if _, err := d.Download(context.Background(), dst, s); err != nil {
			t.Fatalf("download via proxy: %v", err)
		}
		if proxied != 1 {
			t.Errorf("expected 1 proxied request, got %d", proxied)
		}
		assertFileContent(t, filepath.Join(dst, "file.txt"), "via proxy")
	})

	t.Run("host-scoped proxy is used for its host", func(t *testing.T) {
		proxied = 0
		s := settings.Settings{Proxies: []settings.ProxyConfig{
			{Host: "unresolvable.grabber.invalid", URL: proxyURL},
		}}
		dst := t.TempDir()
		d := &Downloader{url: "http://unresolvable.grabber.invalid/file.txt"}
		if _, err := d.Download(context.Background(), dst, s); err != nil {
			t.Fatalf("download via host-scoped proxy: %v", err)
		}
		if proxied != 1 {
			t.Errorf("expected 1 proxied request, got %d", proxied)
		}
	})

	t.Run("host-scoped proxy is not used for other hosts", func(t *testing.T) {
		proxied = 0
		s := settings.Settings{Proxies: []settings.ProxyConfig{
			{Host: "some-other-host.invalid", URL: proxyURL},
		}}
		d := &Downloader{url: "http://unresolvable.grabber.invalid/file.txt"}
		// No proxy matches, so the direct request to an unresolvable host fails.
		if _, err := d.Download(context.Background(), t.TempDir(), s); err == nil {
			t.Fatal("expected direct request to unresolvable host to fail")
		}
		if proxied != 0 {
			t.Errorf("expected 0 proxied requests, got %d", proxied)
		}
	})

	t.Run("proxy credentials are sent", func(t *testing.T) {
		var gotAuth string
		authProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Proxy-Authorization")
			_, _ = w.Write([]byte("ok"))
		}))
		defer authProxy.Close()
		apURL, _ := url.Parse(authProxy.URL)

		s := settings.Settings{Proxies: []settings.ProxyConfig{{URL: apURL, Username: "u", Password: "p"}}}
		d := &Downloader{url: "http://unresolvable.grabber.invalid/file.txt"}
		if _, err := d.Download(context.Background(), t.TempDir(), s); err != nil {
			t.Fatalf("download via authenticated proxy: %v", err)
		}
		if gotAuth == "" {
			t.Error("expected a Proxy-Authorization header")
		}
	})
}
