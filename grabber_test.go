package grabber

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liamg/grabber/ssrf"
)

func TestGrab_FileProtocol_SingleFile(t *testing.T) {
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "hello.txt")
	os.WriteFile(srcFile, []byte("hello world"), 0o644)

	g := New()
	dst := filepath.Join(t.TempDir(), "output")
	err := g.Grab(context.Background(), srcFile, dst)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}

	assertFile(t, filepath.Join(dst, "hello.txt"), "hello world")
}

func TestGrab_FileProtocol_Directory(t *testing.T) {
	srcDir := t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("aaa"), 0o644)
	os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755)
	os.WriteFile(filepath.Join(srcDir, "sub", "b.txt"), []byte("bbb"), 0o644)

	g := New()
	dst := filepath.Join(t.TempDir(), "output")
	err := g.Grab(context.Background(), srcDir, dst)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}

	assertFile(t, filepath.Join(dst, "a.txt"), "aaa")
	assertFile(t, filepath.Join(dst, "sub", "b.txt"), "bbb")
}

func TestGrab_FileProtocol_FileScheme(t *testing.T) {
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "data.txt")
	os.WriteFile(srcFile, []byte("file scheme"), 0o644)

	g := New()
	dst := filepath.Join(t.TempDir(), "output")
	err := g.Grab(context.Background(), "file://"+srcFile, dst)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}

	assertFile(t, filepath.Join(dst, "data.txt"), "file scheme")
}

func TestGrab_FileProtocol_DstExists(t *testing.T) {
	srcDir := t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "new.txt"), []byte("new content"), 0o644)

	// Pre-create dst with existing content.
	dst := t.TempDir()
	os.WriteFile(filepath.Join(dst, "existing.txt"), []byte("existing"), 0o644)

	g := New()
	err := g.Grab(context.Background(), srcDir, dst)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}

	// New content should be copied into existing dst.
	assertFile(t, filepath.Join(dst, "new.txt"), "new content")
	// Existing file should still be there.
	assertFile(t, filepath.Join(dst, "existing.txt"), "existing")
}

func TestGrab_HTTPProtocol(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("http content"))
	}))
	defer srv.Close()

	g := New(WithSSRFProtection(ssrf.None))
	dst := filepath.Join(t.TempDir(), "output")
	err := g.Grab(context.Background(), srv.URL+"/file.txt", dst)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}

	assertFile(t, filepath.Join(dst, "file.txt"), "http content")
}

func TestGrab_AutoExtract_Disabled(t *testing.T) {
	// Create a .gz file — if extraction is disabled, it should be copied as-is.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "data.gz")
	os.WriteFile(srcFile, []byte("not actually gzip"), 0o644)

	g := New(WithAutoExtract(false), WithSSRFProtection(ssrf.None))
	dst := filepath.Join(t.TempDir(), "output")
	err := g.Grab(context.Background(), srcFile, dst)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}

	assertFile(t, filepath.Join(dst, "data.gz"), "not actually gzip")
}

func TestGrab_AutoExtract_ZipFile(t *testing.T) {
	srcDir := t.TempDir()
	zipFile := filepath.Join(srcDir, "archive.zip")
	createTestZip(t, zipFile, map[string]string{
		"inner.txt": "extracted content",
	})

	g := New()
	dst := filepath.Join(t.TempDir(), "output")
	err := g.Grab(context.Background(), zipFile, dst)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}

	assertFile(t, filepath.Join(dst, "inner.txt"), "extracted content")
}

func TestGrab_AutoExtract_HTTPZip(t *testing.T) {
	// Serve a zip file over HTTP — should be downloaded and auto-extracted.
	zipDir := t.TempDir()
	zipFile := filepath.Join(zipDir, "pkg.zip")
	createTestZip(t, zipFile, map[string]string{
		"readme.txt": "hello from zip",
	})
	zipData, _ := os.ReadFile(zipFile)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipData)
	}))
	defer srv.Close()

	g := New(WithSSRFProtection(ssrf.None))
	dst := filepath.Join(t.TempDir(), "output")
	err := g.Grab(context.Background(), srv.URL+"/pkg.zip", dst)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}

	assertFile(t, filepath.Join(dst, "readme.txt"), "hello from zip")
}

func TestGrab_UnsupportedURL(t *testing.T) {
	g := New()
	err := g.Grab(context.Background(), "ftp://example.com/file", t.TempDir())
	if err == nil {
		t.Fatal("expected error for unsupported URL")
	}
}

func TestGrab_ForcePrefix(t *testing.T) {
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "test.txt")
	os.WriteFile(srcFile, []byte("via prefix"), 0o644)

	g := New()
	dst := filepath.Join(t.TempDir(), "output")
	err := g.Grab(context.Background(), "file::"+srcFile, dst)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}

	assertFile(t, filepath.Join(dst, "test.txt"), "via prefix")
}

func TestGrab_ForcePrefix_WrongProtocol(t *testing.T) {
	g := New()
	err := g.Grab(context.Background(), "s3::https://example.com/file.txt", t.TempDir())
	if err == nil {
		t.Fatal("expected error for s3:: prefix with non-S3 URL")
	}
}

func TestNew_Defaults(t *testing.T) {
	g := New()
	if !g.settings.EnableAutoExtract {
		t.Error("expected EnableAutoExtract=true by default")
	}
	if g.settings.Git.SparseCheckout {
		t.Error("expected Git.SparseCheckout=false by default")
	}
	if len(g.protocols) == 0 {
		t.Error("expected default protocols to be registered")
	}
}

func TestNew_WithOptions(t *testing.T) {
	g := New(
		WithSparseCheckout(true),
		WithAutoExtract(false),
		WithGitDepth(5),
		WithAWSCredentials("key", "secret", "token", "us-west-2"),
		WithOCICredentials("user", "pass"),
	)

	if !g.settings.Git.SparseCheckout {
		t.Error("expected Git.SparseCheckout=true")
	}
	if g.settings.EnableAutoExtract {
		t.Error("expected EnableAutoExtract=false")
	}
	if g.settings.Git.Depth != 5 {
		t.Errorf("expected Git.Depth=5, got %d", g.settings.Git.Depth)
	}
	if g.settings.AWSCredentials.AccessKeyID != "key" {
		t.Errorf("expected AWS AccessKeyID=key, got %s", g.settings.AWSCredentials.AccessKeyID)
	}
	if g.settings.AWSCredentials.Region != "us-west-2" {
		t.Errorf("expected AWS Region=us-west-2, got %s", g.settings.AWSCredentials.Region)
	}
	if len(g.settings.OCICredentials) != 1 || g.settings.OCICredentials[0].Username != "user" {
		t.Errorf("expected a default OCI credential with Username=user, got %+v", g.settings.OCICredentials)
	}
}

func TestWithOCICredentialForRegistry(t *testing.T) {
	g := New(
		WithOCICredentials("default-user", "default-pass"),
		WithOCICredentialForRegistry("ghcr.io", "gh-user", "gh-pass"),
	)

	if cred := g.settings.MatchOCICredential("ghcr.io"); cred == nil || cred.Username != "gh-user" {
		t.Errorf("expected ghcr.io to match gh-user, got %+v", cred)
	}
	// A registry with no specific match falls back to the default credential.
	if cred := g.settings.MatchOCICredential("registry.example.com"); cred == nil || cred.Username != "default-user" {
		t.Errorf("expected fallback to default-user, got %+v", cred)
	}
}

func TestWithTLSAndProxyOptions(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.example.com:8080")
	registryProxyURL, _ := url.Parse("http://registry-proxy.example.com:8080")
	tr := http.DefaultTransport.(*http.Transport).Clone()

	g := New(
		WithHTTPTransport(tr),
		WithTLSCACert([]byte("ca-pem")),
		WithTLSCACert([]byte("second-ca")),
		WithClientCertificate([]byte("cert"), []byte("key")),
		WithClientCertificateForHost("github.example.com", []byte("gh-cert"), []byte("gh-key")),
		WithHTTPProxy(proxyURL, "user", "pass"),
		WithHTTPProxyForHost("registry.terraform.io", registryProxyURL, "", ""),
	)

	if g.settings.HTTPTransport != tr {
		t.Error("expected HTTPTransport to be set")
	}
	if len(g.settings.TLSCACerts) != 2 {
		t.Errorf("expected 2 CA certs, got %d", len(g.settings.TLSCACerts))
	}
	if c := g.settings.MatchClientCertificate("github.example.com"); c == nil || string(c.Cert) != "gh-cert" {
		t.Errorf("expected host-scoped cert for github.example.com, got %+v", c)
	}
	if c := g.settings.MatchClientCertificate("other.example.com"); c == nil || string(c.Cert) != "cert" {
		t.Errorf("expected default cert fallback, got %+v", c)
	}
	if p := g.settings.MatchProxy("registry.terraform.io"); p == nil || p.URL != registryProxyURL {
		t.Errorf("expected host-scoped proxy, got %+v", p)
	}
	if p := g.settings.MatchProxy("github.com"); p == nil || p.URL != proxyURL || p.Username != "user" {
		t.Errorf("expected global proxy fallback, got %+v", p)
	}
}

func TestSSRFProtectionOptions(t *testing.T) {
	if lvl := New().settings.SSRFLevel; lvl != ssrf.Default {
		t.Errorf("expected zero-value (Default) level, got %v", lvl)
	}
	// The zero value resolves to an enabled (Internal) guard.
	if !New().settings.SSRFGuard().Enabled() {
		t.Error("expected the default guard to be enabled")
	}
	if lvl := New(WithSSRFProtection(ssrf.None)).settings.SSRFLevel; lvl != ssrf.None {
		t.Errorf("expected None, got %v", lvl)
	}
	g := New(WithCustomSSRFProtection(func(net.IP) bool { return true }))
	if g.settings.SSRFLevel != ssrf.Custom || g.settings.SSRFCustom == nil {
		t.Error("expected Custom level with a predicate set")
	}
}

func TestGrab_SSRFProtection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("loopback content"))
	}))
	defer srv.Close()

	t.Run("blocks loopback by default", func(t *testing.T) {
		g := New()
		err := g.Grab(context.Background(), srv.URL+"/file.txt", t.TempDir())
		if err == nil {
			t.Fatal("expected the default SSRF guard to block a loopback download")
		}
	})

	t.Run("allowed when disabled", func(t *testing.T) {
		g := New(WithSSRFProtection(ssrf.None))
		dst := t.TempDir()
		if err := g.Grab(context.Background(), srv.URL+"/file.txt", dst); err != nil {
			t.Fatalf("expected success with SSRF disabled: %v", err)
		}
		assertFile(t, filepath.Join(dst, "file.txt"), "loopback content")
	})

	t.Run("custom predicate can block", func(t *testing.T) {
		g := New(WithCustomSSRFProtection(func(net.IP) bool { return true }))
		if err := g.Grab(context.Background(), srv.URL+"/file.txt", t.TempDir()); err == nil {
			t.Fatal("expected the custom predicate to block the download")
		}
	})

	t.Run("allowlisted host is permitted despite the default guard", func(t *testing.T) {
		g := New(WithSSRFAllowHosts("127.0.0.1"))
		dst := t.TempDir()
		if err := g.Grab(context.Background(), srv.URL+"/file.txt", dst); err != nil {
			t.Fatalf("expected allowlisted loopback to be permitted: %v", err)
		}
		assertFile(t, filepath.Join(dst, "file.txt"), "loopback content")
	})
}

func TestWithSSRFAllowHosts(t *testing.T) {
	g := New(WithSSRFAllowHosts("a.example.com", "10.0.0.0/8"), WithSSRFAllowHosts("1.2.3.4"))
	want := []string{"a.example.com", "10.0.0.0/8", "1.2.3.4"}
	if len(g.settings.SSRFAllow) != len(want) {
		t.Fatalf("expected %d allow entries, got %v", len(want), g.settings.SSRFAllow)
	}
	for i, w := range want {
		if g.settings.SSRFAllow[i] != w {
			t.Errorf("allow[%d] = %q, want %q", i, g.settings.SSRFAllow[i], w)
		}
	}
}

func TestWithHTTPCredentialRequestFunction(t *testing.T) {
	if New().settings.HTTPCredentialRequest != nil {
		t.Error("expected no credential function by default")
	}
	g := New(WithHTTPCredentialRequestFunction(func(_ context.Context, _, _, _ string) (*string, *string, bool) {
		u, p := "u", "p"
		return &u, &p, true
	}))
	if g.settings.HTTPCredentialRequest == nil {
		t.Fatal("expected the credential function to be set")
	}
	user, pass, ok := g.settings.RequestCredential(context.Background(), "https", "h", "/p")
	if !ok || user != "u" || pass != "p" {
		t.Errorf("got (%q,%q,%v), want (u,p,true)", user, pass, ok)
	}
}

func TestWithConnectProbeTimeout(t *testing.T) {
	if d := New().settings.ConnectProbeTimeout; d != 0 {
		t.Errorf("expected 0 by default, got %v", d)
	}
	if d := New(WithConnectProbeTimeout(3 * time.Second)).settings.ConnectProbeTimeout; d != 3*time.Second {
		t.Errorf("expected 3s, got %v", d)
	}
}

func TestWithNoSystemFallback(t *testing.T) {
	if g := New(); g.settings.NoSystemFallback {
		t.Error("expected NoSystemFallback=false by default")
	}
	if g := New(WithNoSystemFallback()); !g.settings.NoSystemFallback {
		t.Error("expected NoSystemFallback=true after WithNoSystemFallback()")
	}
}

func TestWithGitKnownHosts(t *testing.T) {
	kh := []byte("example.com ssh-ed25519 AAAA...")
	g := New(WithGitKnownHosts(kh))
	if !bytes.Equal(g.settings.Git.KnownHosts, kh) {
		t.Errorf("expected KnownHosts to be set, got %q", g.settings.Git.KnownHosts)
	}
}

func TestWithGitSSHKeyForHost(t *testing.T) {
	defaultKey := []byte("default-key")
	ghKey := []byte("github-key")
	g := New(
		WithGitSSHKey(defaultKey),
		WithGitSSHKeyForHost("github.com", ghKey),
	)

	if got := g.settings.MatchSSHKey("github.com"); !bytes.Equal(got, ghKey) {
		t.Errorf("expected github.com to match github-key, got %q", got)
	}
	// Case-insensitive host matching.
	if got := g.settings.MatchSSHKey("GitHub.com"); !bytes.Equal(got, ghKey) {
		t.Errorf("expected case-insensitive match to github-key, got %q", got)
	}
	// An unmatched host falls back to the default key.
	if got := g.settings.MatchSSHKey("gitlab.com"); !bytes.Equal(got, defaultKey) {
		t.Errorf("expected fallback to default-key, got %q", got)
	}
}

func TestProtocolPriority(t *testing.T) {
	g := New()
	for i := 1; i < len(g.protocols); i++ {
		prev := g.protocols[i-1].Priority()
		curr := g.protocols[i].Priority()
		if prev < curr {
			t.Errorf("protocols not sorted by priority: %s(%d) before %s(%d)",
				g.protocols[i-1].Prefix(), prev,
				g.protocols[i].Prefix(), curr)
		}
	}
}

func TestGrab_ChecksumFromURL(t *testing.T) {
	content := []byte("checksum test content")
	h := sha256.Sum256(content)
	checksum := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	g := New(WithAutoExtract(false), WithSSRFProtection(ssrf.None))
	dst := filepath.Join(t.TempDir(), "output")
	err := g.Grab(context.Background(), srv.URL+"/file.txt?checksum=sha256:"+checksum, dst)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}

	assertFile(t, filepath.Join(dst, "file.txt"), string(content))
}

func TestGrab_ChecksumFromURL_Mismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("actual content"))
	}))
	defer srv.Close()

	g := New(WithAutoExtract(false), WithSSRFProtection(ssrf.None))
	dst := filepath.Join(t.TempDir(), "output")
	err := g.Grab(context.Background(), srv.URL+"/file.txt?checksum=sha256:0000000000000000000000000000000000000000000000000000000000000000", dst)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}

func TestGrabWithSHA256Checksum_Match(t *testing.T) {
	content := []byte("explicit checksum content")
	h := sha256.Sum256(content)
	checksum := hex.EncodeToString(h[:])

	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "data.bin")
	os.WriteFile(srcFile, content, 0o644)

	g := New(WithAutoExtract(false), WithSSRFProtection(ssrf.None))
	dst := filepath.Join(t.TempDir(), "output")
	err := g.GrabWithSHA256Checksum(context.Background(), srcFile, dst, checksum)
	if err != nil {
		t.Fatalf("GrabWithSHA256Checksum: %v", err)
	}

	assertFile(t, filepath.Join(dst, "data.bin"), string(content))
}

func TestGrabWithSHA256Checksum_Mismatch(t *testing.T) {
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "data.bin")
	os.WriteFile(srcFile, []byte("some content"), 0o644)

	g := New(WithAutoExtract(false), WithSSRFProtection(ssrf.None))
	dst := filepath.Join(t.TempDir(), "output")
	err := g.GrabWithSHA256Checksum(context.Background(), srcFile, dst, "0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}

func TestGrabWithSHA256Checksum_OverridesURL(t *testing.T) {
	content := []byte("override test")
	h := sha256.Sum256(content)
	correctChecksum := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	g := New(WithAutoExtract(false), WithSSRFProtection(ssrf.None))
	dst := filepath.Join(t.TempDir(), "output")
	// URL has a wrong checksum, but explicit is correct — should succeed.
	err := g.GrabWithSHA256Checksum(context.Background(),
		srv.URL+"/file.txt?checksum=sha256:0000000000000000000000000000000000000000000000000000000000000000",
		dst, correctChecksum)
	if err != nil {
		t.Fatalf("GrabWithSHA256Checksum: %v", err)
	}
}

func TestGrabWithSHA256Checksum(t *testing.T) {
	content := []byte("sha256 convenience method")
	h := sha256.Sum256(content)
	checksum := hex.EncodeToString(h[:])

	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "data.bin")
	os.WriteFile(srcFile, content, 0o644)

	g := New(WithAutoExtract(false), WithSSRFProtection(ssrf.None))
	dst := filepath.Join(t.TempDir(), "output")
	err := g.GrabWithSHA256Checksum(context.Background(), srcFile, dst, checksum)
	if err != nil {
		t.Fatalf("GrabWithSHA256Checksum: %v", err)
	}

	assertFile(t, filepath.Join(dst, "data.bin"), string(content))
}

func TestGrab_ChecksumFromURL_DefaultSHA256(t *testing.T) {
	content := []byte("default sha256 test")
	h := sha256.Sum256(content)
	checksum := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	g := New(WithAutoExtract(false), WithSSRFProtection(ssrf.None))
	dst := filepath.Join(t.TempDir(), "output")
	// No "sha256:" prefix — should default to sha256.
	err := g.Grab(context.Background(), srv.URL+"/file.txt?checksum="+checksum, dst)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}

	assertFile(t, filepath.Join(dst, "file.txt"), string(content))
}

func TestGrab_ChecksumOnDirectory_Error(t *testing.T) {
	srcDir := t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("aaa"), 0o644)

	g := New()
	dst := filepath.Join(t.TempDir(), "output")
	err := g.Grab(context.Background(), srcDir+"?checksum=sha256:abc123", dst)
	if err == nil {
		t.Fatal("expected error for checksum on directory download")
	}
}

func TestExtractChecksum(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		wantURL       string
		wantAlgorithm string
		wantExpected  string
	}{
		{
			name:          "sha256 checksum",
			url:           "https://example.com/file.tar.gz?checksum=sha256:abcdef1234567890",
			wantURL:       "https://example.com/file.tar.gz",
			wantAlgorithm: "sha256",
			wantExpected:  "abcdef1234567890",
		},
		{
			name:    "no checksum",
			url:     "https://example.com/file.tar.gz",
			wantURL: "https://example.com/file.tar.gz",
		},
		{
			name:          "checksum with other params",
			url:           "https://example.com/file.tar.gz?ref=main&checksum=md5:abc123&depth=1",
			wantURL:       "https://example.com/file.tar.gz?depth=1&ref=main",
			wantAlgorithm: "md5",
			wantExpected:  "abc123",
		},
		{
			name:          "with force prefix",
			url:           "s3::https://example.com/file.tar.gz?checksum=sha512:deadbeef",
			wantURL:       "s3::https://example.com/file.tar.gz",
			wantAlgorithm: "sha512",
			wantExpected:  "deadbeef",
		},
		{
			name:          "no algorithm prefix defaults to sha256",
			url:           "https://example.com/file.tar.gz?checksum=abcdef1234567890",
			wantURL:       "https://example.com/file.tar.gz",
			wantAlgorithm: "sha256",
			wantExpected:  "abcdef1234567890",
		},
		{
			name:    "empty checksum value",
			url:     "https://example.com/file.tar.gz?checksum=",
			wantURL: "https://example.com/file.tar.gz?checksum=",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotChecksum := extractChecksum(tt.url)
			if gotURL != tt.wantURL {
				t.Errorf("url = %q, want %q", gotURL, tt.wantURL)
			}
			if gotChecksum.algorithm != tt.wantAlgorithm {
				t.Errorf("algorithm = %q, want %q", gotChecksum.algorithm, tt.wantAlgorithm)
			}
			if gotChecksum.expected != tt.wantExpected {
				t.Errorf("expected = %q, want %q", gotChecksum.expected, tt.wantExpected)
			}
		})
	}
}

// --- helpers ---

func assertFile(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(data) != expected {
		t.Errorf("%s: got %q, want %q", path, string(data), expected)
	}
}

func createTestZip(t *testing.T, dst string, files map[string]string) {
	t.Helper()
	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	zw.Close()
}
