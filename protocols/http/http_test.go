package http

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liamg/grabber/settings"
	"github.com/liamg/grabber/ssrf"
)

// withoutSSRF disables the SSRF guard, for tests that dial loopback servers and
// are not exercising the guard itself.
func withoutSSRF(s settings.Settings) settings.Settings {
	s.SSRFLevel = ssrf.None
	return s
}

func TestDetect(t *testing.T) {
	p := New()

	tests := []struct {
		name  string
		url   string
		match bool
	}{
		{"https url", "https://example.com/file.tar.gz", true},
		{"http url", "http://example.com/file.tar.gz", true},
		{"no scheme", "example.com/file.tar.gz", true},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := p.Detect(tt.url)
			if ok != tt.match {
				t.Errorf("Detect(%q) = %v, want %v", tt.url, ok, tt.match)
			}
		})
	}
}

func TestParseHTTPURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantURL string
		wantErr bool
	}{
		{"https", "https://example.com/file.tar.gz", "https://example.com/file.tar.gz", false},
		{"http", "http://example.com/file.tar.gz", "http://example.com/file.tar.gz", false},
		{"no scheme defaults to https", "example.com/file.tar.gz", "https://example.com/file.tar.gz", false},
		{"with path", "https://example.com/path/to/file.zip", "https://example.com/path/to/file.zip", false},
		{"ssh scheme rejected", "ssh://example.com/file", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := parseHTTPURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseHTTPURL(%q) expected error", tt.url)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseHTTPURL(%q) unexpected error: %v", tt.url, err)
			}
			if d.url != tt.wantURL {
				t.Errorf("parseHTTPURL(%q) url = %q, want %q", tt.url, d.url, tt.wantURL)
			}
		})
	}
}

func TestParseHTTPURL_StripsArchiveParam(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		wantURL     string
		wantArchive string
	}{
		{
			// The parameter directs the getter; the server knows nothing of it.
			name:        "archive is removed from the request URL",
			url:         "https://example.com/v1/object/abc?archive=tgz",
			wantURL:     "https://example.com/v1/object/abc",
			wantArchive: "tgz",
		},
		{
			name:        "other parameters survive",
			url:         "https://example.com/f?archive=zip&token=xyz",
			wantURL:     "https://example.com/f?token=xyz",
			wantArchive: "zip",
		},
		{
			name:        "absent leaves the URL untouched",
			url:         "https://example.com/file.tar.gz",
			wantURL:     "https://example.com/file.tar.gz",
			wantArchive: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := parseHTTPURL(tt.url)
			if err != nil {
				t.Fatalf("parseHTTPURL(%q) unexpected error: %v", tt.url, err)
			}
			if d.url != tt.wantURL {
				t.Errorf("url = %q, want %q", d.url, tt.wantURL)
			}
			if d.archive != tt.wantArchive {
				t.Errorf("archive = %q, want %q", d.archive, tt.wantArchive)
			}
		})
	}
}

func TestFileName(t *testing.T) {
	longSegment := strings.Repeat("A", 400)

	tests := []struct {
		name    string
		dl      *Downloader
		want    string
		wantLen int
	}{
		{
			name: "URL path supplies the name",
			dl:   &Downloader{url: "https://example.com/path/module.tar.gz"},
			want: "module.tar.gz",
		},
		{
			name: "no path falls back",
			dl:   &Downloader{url: "https://example.com"},
			want: "download",
		},
		{
			// A Terraform Cloud registry download: opaque token, no extension.
			name:    "archive parameter names the format",
			dl:      &Downloader{url: "https://archivist.terraform.io/v1/object/" + longSegment, archive: "tgz"},
			want:    "archive.tgz",
			wantLen: len("archive.tgz"),
		},
		{
			name: "archive parameter wins over the path",
			dl:   &Downloader{url: "https://example.com/thing.zip", archive: "tar.gz"},
			want: "archive.tar.gz",
		},
		{
			// A boolean disables extraction rather than naming a format.
			name: "archive=false does not become an extension",
			dl:   &Downloader{url: "https://example.com/thing", archive: "false"},
			want: "download",
		},
		{
			name: "archive=true does not become an extension",
			dl:   &Downloader{url: "https://example.com/thing", archive: "true"},
			want: "download",
		},
		{
			// Must be creatable: NAME_MAX is 255 on the filesystems we target.
			name:    "overlong path segment is clamped",
			dl:      &Downloader{url: "https://example.com/v1/object/" + longSegment},
			wantLen: maxFileName,
		},
		{
			name:    "clamping keeps the extension, which selects the extractor",
			dl:      &Downloader{url: "https://example.com/" + longSegment + ".tar.gz"},
			wantLen: maxFileName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.dl.fileName()

			if tt.want != "" && got != tt.want {
				t.Errorf("fileName() = %q, want %q", got, tt.want)
			}
			if tt.wantLen != 0 && len(got) != tt.wantLen {
				t.Errorf("len(fileName()) = %d, want %d", len(got), tt.wantLen)
			}
			if len(got) > maxFileName {
				t.Errorf("fileName() is %d bytes, exceeds NAME_MAX %d", len(got), maxFileName)
			}
		})
	}

	t.Run("clamped name still ends in the archive extension", func(t *testing.T) {
		d := &Downloader{url: "https://example.com/" + longSegment + ".tar.gz"}
		if got := d.fileName(); !strings.HasSuffix(got, ".tar.gz") {
			t.Errorf("fileName() = %q, want a .tar.gz suffix", got)
		}
	})
}

func TestDownload_ArchiveParamNamesTheFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("archive") {
			t.Errorf("archive parameter reached the server: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	// Mirrors a Terraform Cloud archivist URL: a long opaque segment, no
	// extension, format carried in the query.
	d, err := parseHTTPURL(srv.URL + "/v1/object/" + strings.Repeat("A", 400) + "?archive=tgz")
	if err != nil {
		t.Fatalf("parseHTTPURL() error: %v", err)
	}

	tmpDir := t.TempDir()
	if _, err := d.Download(context.Background(), tmpDir, withoutSSRF(settings.Defaults)); err != nil {
		t.Fatalf("Download() error: %v", err)
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir() error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, found %d", len(entries))
	}
	if entries[0].Name() != "archive.tgz" {
		t.Errorf("wrote %q, want %q", entries[0].Name(), "archive.tgz")
	}
}

func TestDownload(t *testing.T) {
	content := "hello world"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(content))
	}))
	defer srv.Close()

	d := &Downloader{url: srv.URL + "/test-file.txt"}
	tmpDir := t.TempDir()

	isFile, err := d.Download(context.Background(), tmpDir, withoutSSRF(settings.Defaults))
	if err != nil {
		t.Fatalf("Download() error: %v", err)
	}
	if !isFile {
		t.Error("Download() isFile = false, want true")
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "test-file.txt"))
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(data) != content {
		t.Errorf("downloaded content = %q, want %q", string(data), content)
	}
}

// recordingTransport delegates to a base transport but records that it was
// used, so we can assert the injected transport is actually exercised. The
// caller-supplied transport customises DialContext (the same seam an SSRF
// guard uses), and grabber must dial through it.
func TestDownload_UsesHTTPTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))
	defer srv.Close()

	var dialed bool
	tr := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = true
		return dialer.DialContext(ctx, network, addr)
	}

	s := withoutSSRF(settings.Defaults)
	s.HTTPTransport = tr

	d := &Downloader{url: srv.URL + "/file.txt"}
	if _, err := d.Download(context.Background(), t.TempDir(), s); err != nil {
		t.Fatalf("Download() error: %v", err)
	}
	if !dialed {
		t.Error("configured HTTPTransport was not used")
	}
}

// TestDownload_HTTPTransportCanBlock stands in for an SSRF guard that refuses
// to dial an internal address via the transport's DialContext.
func TestDownload_HTTPTransportCanBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("should not be reached"))
	}))
	defer srv.Close()

	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("blocked by transport")
	}

	s := withoutSSRF(settings.Defaults)
	s.HTTPTransport = tr

	d := &Downloader{url: srv.URL + "/file.txt"}
	if _, err := d.Download(context.Background(), t.TempDir(), s); err == nil {
		t.Error("expected error when transport blocks the request, got nil")
	}
}

func TestDownload_DynamicCredentials(t *testing.T) {
	var gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	t.Run("used when no static credential matches", func(t *testing.T) {
		var gotProto, gotHost, gotPath string
		s := withoutSSRF(settings.Defaults)
		s.HTTPCredentialRequest = func(_ context.Context, protocol, host, path string) (*string, *string, bool) {
			gotProto, gotHost, gotPath = protocol, host, path
			u, p := "dyn-user", "dyn-pass"
			return &u, &p, true
		}
		d := &Downloader{url: srv.URL + "/a/b.txt"}
		if _, err := d.Download(context.Background(), t.TempDir(), s); err != nil {
			t.Fatalf("Download: %v", err)
		}
		if gotUser != "dyn-user" || gotPass != "dyn-pass" {
			t.Errorf("server saw (%q,%q), want dynamic creds", gotUser, gotPass)
		}
		if gotProto != "http" || gotHost != "127.0.0.1" || gotPath != "/a/b.txt" {
			t.Errorf("callback args = (%q,%q,%q)", gotProto, gotHost, gotPath)
		}
	})

	t.Run("static credential wins over dynamic", func(t *testing.T) {
		s := withoutSSRF(settings.Defaults)
		s.HTTPSCredentials = []settings.HTTPSCredential{{Host: "127.0.0.1", Username: "static-user", Password: "static-pass"}}
		s.HTTPCredentialRequest = func(context.Context, string, string, string) (*string, *string, bool) {
			t.Error("dynamic function must not be consulted when a static credential matches")
			return nil, nil, false
		}
		d := &Downloader{url: srv.URL + "/file.txt"}
		if _, err := d.Download(context.Background(), t.TempDir(), s); err != nil {
			t.Fatalf("Download: %v", err)
		}
		if gotUser != "static-user" {
			t.Errorf("server saw user %q, want static-user", gotUser)
		}
	})

	t.Run("declining function sends no credentials", func(t *testing.T) {
		s := withoutSSRF(settings.Defaults)
		s.HTTPCredentialRequest = func(context.Context, string, string, string) (*string, *string, bool) {
			return nil, nil, false
		}
		d := &Downloader{url: srv.URL + "/file.txt"}
		if _, err := d.Download(context.Background(), t.TempDir(), s); err != nil {
			t.Fatalf("Download: %v", err)
		}
		if gotUser != "" || gotPass != "" {
			t.Errorf("expected no credentials, server saw (%q,%q)", gotUser, gotPass)
		}
	})
}

func TestDownload_ConnectProbe(t *testing.T) {
	t.Run("unreachable host fails fast via the probe", func(t *testing.T) {
		s := withoutSSRF(settings.Defaults)
		s.ConnectProbeTimeout = 500 * time.Millisecond
		// Port 1 on loopback is closed → the probe fails before any GET.
		d := &Downloader{url: "http://127.0.0.1:1/file.txt"}
		start := time.Now()
		_, err := d.Download(context.Background(), t.TempDir(), s)
		if err == nil {
			t.Fatal("expected the probe to fail for an unreachable host")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("expected a fast failure, took %v", elapsed)
		}
	})

	t.Run("reachable host proceeds", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("ok"))
		}))
		defer srv.Close()

		s := withoutSSRF(settings.Defaults)
		s.ConnectProbeTimeout = 2 * time.Second
		d := &Downloader{url: srv.URL + "/file.txt"}
		if _, err := d.Download(context.Background(), t.TempDir(), s); err != nil {
			t.Fatalf("expected reachable host to succeed: %v", err)
		}
	})
}

func TestDownload_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	d := &Downloader{url: srv.URL + "/missing.txt"}
	tmpDir := t.TempDir()

	_, err := d.Download(context.Background(), tmpDir, withoutSSRF(settings.Defaults))
	if err == nil {
		t.Error("Download() expected error for 404")
	}
}

func TestDownload_WithHTTPSCredentials(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("secret content"))
	}))
	defer srv.Close()

	d := &Downloader{url: srv.URL + "/private/file.txt"}
	tmpDir := t.TempDir()

	s := withoutSSRF(settings.Defaults)
	s.HTTPSCredentials = []settings.HTTPSCredential{
		{Host: "127.0.0.1", Username: "myuser", Password: "mypass"},
	}

	_, err := d.Download(context.Background(), tmpDir, s)
	if err != nil {
		t.Fatalf("Download() error: %v", err)
	}

	if gotAuth == "" {
		t.Fatal("expected Authorization header to be set")
	}

	// Verify the file was downloaded.
	data, err := os.ReadFile(filepath.Join(tmpDir, "file.txt"))
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(data) != "secret content" {
		t.Errorf("downloaded content = %q, want %q", string(data), "secret content")
	}
}

func TestDownload_Netrc(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("content"))
	}))
	defer srv.Close()

	hostname, _, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	netrcPath := filepath.Join(t.TempDir(), ".netrc")
	if err := os.WriteFile(netrcPath, []byte("machine "+hostname+" login netrc-user password netrc-pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NETRC", netrcPath)

	d := &Downloader{url: srv.URL + "/file.txt"}
	s := withoutSSRF(settings.Defaults)
	s.Netrc = true

	if _, err := d.Download(context.Background(), t.TempDir(), s); err != nil {
		t.Fatalf("Download() error: %v", err)
	}

	user, pass, ok := parseBasicAuth(gotAuth)
	if !ok || user != "netrc-user" || pass != "netrc-pass" {
		t.Errorf("netrc credentials not applied: got auth %q", gotAuth)
	}
}

func TestDownload_NetrcDisabled(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("content"))
	}))
	defer srv.Close()

	hostname, _, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	netrcPath := filepath.Join(t.TempDir(), ".netrc")
	if err := os.WriteFile(netrcPath, []byte("machine "+hostname+" login u password p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NETRC", netrcPath)

	d := &Downloader{url: srv.URL + "/file.txt"}
	// settings.Defaults has Netrc=false — netrc must be ignored.
	if _, err := d.Download(context.Background(), t.TempDir(), withoutSSRF(settings.Defaults)); err != nil {
		t.Fatalf("Download() error: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("expected no auth when netrc disabled, got %q", gotAuth)
	}
}

func parseBasicAuth(header string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(header[len(prefix):])
	if err != nil {
		return "", "", false
	}
	user, pass, ok = strings.Cut(string(decoded), ":")
	return user, pass, ok
}

func TestDownload_WithHTTPSCredentials_NoMatch(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("public content"))
	}))
	defer srv.Close()

	d := &Downloader{url: srv.URL + "/public/file.txt"}
	tmpDir := t.TempDir()

	s := withoutSSRF(settings.Defaults)
	s.HTTPSCredentials = []settings.HTTPSCredential{
		{Host: "other-host.com", Username: "myuser", Password: "mypass"},
	}

	_, err := d.Download(context.Background(), tmpDir, s)
	if err != nil {
		t.Fatalf("Download() error: %v", err)
	}

	if gotAuth != "" {
		t.Errorf("expected no Authorization header, got %q", gotAuth)
	}
}

func TestParseHTTPURL_RejectsFilePaths(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"absolute path", "/etc/passwd"},
		{"relative dot", "./local/file.txt"},
		{"relative parent", "../other/file.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseHTTPURL(tt.url)
			if err == nil {
				t.Errorf("parseHTTPURL(%q) expected error for file path", tt.url)
			}
		})
	}
}

func TestPrefix(t *testing.T) {
	p := New()
	if p.Prefix() != "http" {
		t.Errorf("Prefix() = %q, want %q", p.Prefix(), "http")
	}
}

func TestPriority(t *testing.T) {
	p := New()
	if p.Priority() != 20 {
		t.Errorf("Priority() = %d, want %d", p.Priority(), 20)
	}
}
