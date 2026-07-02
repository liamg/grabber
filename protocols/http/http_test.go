package http

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/liamg/grabber/settings"
)

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

func TestDownload(t *testing.T) {
	content := "hello world"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(content))
	}))
	defer srv.Close()

	d := &Downloader{url: srv.URL + "/test-file.txt"}
	tmpDir := t.TempDir()

	isFile, err := d.Download(context.Background(), tmpDir, settings.Defaults)
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

	s := settings.Defaults
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

	s := settings.Defaults
	s.HTTPTransport = tr

	d := &Downloader{url: srv.URL + "/file.txt"}
	if _, err := d.Download(context.Background(), t.TempDir(), s); err == nil {
		t.Error("expected error when transport blocks the request, got nil")
	}
}

func TestDownload_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	d := &Downloader{url: srv.URL + "/missing.txt"}
	tmpDir := t.TempDir()

	_, err := d.Download(context.Background(), tmpDir, settings.Defaults)
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

	s := settings.Defaults
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

func TestDownload_WithHTTPSCredentials_NoMatch(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("public content"))
	}))
	defer srv.Close()

	d := &Downloader{url: srv.URL + "/public/file.txt"}
	tmpDir := t.TempDir()

	s := settings.Defaults
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
