package git

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/liamg/grabber/internal/testcert"
	"github.com/liamg/grabber/settings"
)

// newGitHTTPSServer serves the given bare repo over the git smart-HTTP
// protocol behind TLS, using `git http-backend` via CGI. The repo is reachable
// at <server URL>/repo.git. If mTLS is true the server requires a client
// certificate signed by the same CA.
func newGitHTTPSServer(t *testing.T, ca *testcert.CA, bareRepo string, mTLS bool) *httptest.Server {
	t.Helper()

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not found; skipping smart-HTTP server test")
	}

	root := t.TempDir()
	if err := os.Rename(bareRepo, filepath.Join(root, "repo.git")); err != nil {
		t.Fatalf("moving bare repo: %v", err)
	}

	handler := &cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}

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
	if mTLS {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(ca.CertPEM())
		srv.TLS.ClientCAs = pool
		srv.TLS.ClientAuth = tls.RequireAndVerifyClientCert
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// TestDownload_GitOverHTTPS_CustomCA proves a real go-git clone verifies the
// server certificate against the configured CA (the GIT_SSL_CAINFO
// replacement).
func TestDownload_GitOverHTTPS_CustomCA(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ca, err := testcert.NewCA()
	if err != nil {
		t.Fatalf("creating CA: %v", err)
	}
	srv := newGitHTTPSServer(t, ca, createBareRepo(t), false)
	repoURL := srv.URL + "/repo.git"

	t.Run("fails without the CA", func(t *testing.T) {
		d := &Downloader{repoURL: repoURL}
		if _, err := d.Download(context.Background(), t.TempDir(), settings.Settings{NoSystemFallback: true}); err == nil {
			t.Fatal("expected TLS verification failure without the custom CA")
		}
	})

	t.Run("succeeds with the CA", func(t *testing.T) {
		s := settings.Settings{
			NoSystemFallback: true,
			TLSCACerts:       [][]byte{ca.CertPEM()},
		}
		dst := t.TempDir()
		d := &Downloader{repoURL: repoURL}
		if _, err := d.Download(context.Background(), dst, s); err != nil {
			t.Fatalf("clone with custom CA: %v", err)
		}
		assertFileContains(t, filepath.Join(dst, "file.txt"), "hello")
	})
}

// TestDownload_GitOverHTTPS_MutualTLS proves a real go-git clone presents the
// configured client certificate (the GIT_SSL_CERT/GIT_SSL_KEY replacement).
func TestDownload_GitOverHTTPS_MutualTLS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ca, err := testcert.NewCA()
	if err != nil {
		t.Fatalf("creating CA: %v", err)
	}
	srv := newGitHTTPSServer(t, ca, createBareRepo(t), true)
	repoURL := srv.URL + "/repo.git"

	clientCert, clientKey, err := ca.IssueClient("grabber-git-client")
	if err != nil {
		t.Fatalf("issuing client cert: %v", err)
	}

	t.Run("fails without a client certificate", func(t *testing.T) {
		s := settings.Settings{
			NoSystemFallback: true,
			TLSCACerts:       [][]byte{ca.CertPEM()},
		}
		d := &Downloader{repoURL: repoURL}
		if _, err := d.Download(context.Background(), t.TempDir(), s); err == nil {
			t.Fatal("expected failure without a client certificate")
		}
	})

	t.Run("succeeds with a host-scoped client certificate", func(t *testing.T) {
		s := settings.Settings{
			NoSystemFallback: true,
			TLSCACerts:       [][]byte{ca.CertPEM()},
			ClientCertificates: []settings.ClientCertificate{
				{Host: "127.0.0.1", Cert: clientCert, Key: clientKey},
			},
		}
		dst := t.TempDir()
		d := &Downloader{repoURL: repoURL}
		if _, err := d.Download(context.Background(), dst, s); err != nil {
			t.Fatalf("clone with client cert: %v", err)
		}
		assertFileContains(t, filepath.Join(dst, "file.txt"), "hello")
	})
}
