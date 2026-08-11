package gcs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	"google.golang.org/api/storage/v1"

	"github.com/liamg/grabber/settings"
)

func TestDetect(t *testing.T) {
	p := New()

	tests := []struct {
		name  string
		url   string
		match bool
	}{
		{"storage.googleapis.com path-style", "storage.googleapis.com/bucket/key", true},
		{"storage.googleapis.com with https", "https://storage.googleapis.com/bucket/key", true},
		{"storage.cloud.google.com", "https://storage.cloud.google.com/bucket/key", true},
		{"virtual-hosted", "bucket.storage.googleapis.com/key", true},
		{"virtual-hosted with https", "https://bucket.storage.googleapis.com/key", true},
		{"not gcs", "https://example.com/file", false},
		{"s3 url", "https://s3.amazonaws.com/bucket/key", false},
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

func TestParseGCSURL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantBucket string
		wantKey    string
		wantErr    bool
	}{
		{
			name:       "path-style googleapis",
			url:        "https://storage.googleapis.com/my-bucket/path/to/file.txt",
			wantBucket: "my-bucket",
			wantKey:    "path/to/file.txt",
		},
		{
			name:       "path-style cloud.google.com",
			url:        "https://storage.cloud.google.com/my-bucket/path/to/file.txt",
			wantBucket: "my-bucket",
			wantKey:    "path/to/file.txt",
		},
		{
			name:       "path-style no scheme",
			url:        "storage.googleapis.com/my-bucket/file.txt",
			wantBucket: "my-bucket",
			wantKey:    "file.txt",
		},
		{
			name:       "virtual-hosted",
			url:        "https://my-bucket.storage.googleapis.com/path/to/file.txt",
			wantBucket: "my-bucket",
			wantKey:    "path/to/file.txt",
		},
		{
			name:       "virtual-hosted no scheme",
			url:        "my-bucket.storage.googleapis.com/file.txt",
			wantBucket: "my-bucket",
			wantKey:    "file.txt",
		},
		{
			name:       "bucket only path-style",
			url:        "https://storage.googleapis.com/my-bucket",
			wantBucket: "my-bucket",
			wantKey:    "",
		},
		{
			name:       "bucket only virtual-hosted",
			url:        "https://my-bucket.storage.googleapis.com/",
			wantBucket: "my-bucket",
			wantKey:    "",
		},
		{
			name:       "directory prefix",
			url:        "https://storage.googleapis.com/my-bucket/dir/",
			wantBucket: "my-bucket",
			wantKey:    "dir/",
		},
		{
			name:    "not gcs",
			url:     "https://example.com/file",
			wantErr: true,
		},
		{
			name:    "no bucket path-style",
			url:     "https://storage.googleapis.com/",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := parseGCSURL(tt.url, false)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseGCSURL(%q) expected error", tt.url)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGCSURL(%q) unexpected error: %v", tt.url, err)
			}
			if d.bucket != tt.wantBucket {
				t.Errorf("bucket = %q, want %q", d.bucket, tt.wantBucket)
			}
			if d.key != tt.wantKey {
				t.Errorf("key = %q, want %q", d.key, tt.wantKey)
			}
		})
	}
}

// TestDetectForced covers a storage-compatible endpoint, which carries no
// googleapis.com name for auto-detection to recognise. The host has to survive
// into the downloader: dropping it would send the request to Google instead of
// where the caller pointed.
func TestDetectForced(t *testing.T) {
	p := New()

	const custom = "https://gcs.example.com/my-bucket/path/to/object"
	if _, ok := p.Detect(custom); ok {
		t.Error("Detect accepted an unknown host; auto-detection must stay strict")
	}
	d, ok := p.DetectForced(custom)
	if !ok {
		t.Fatal("DetectForced rejected a custom endpoint")
	}
	dl := d.(*Downloader)
	if dl.bucket != "my-bucket" || dl.key != "path/to/object" {
		t.Errorf("bucket/key = %q/%q, want my-bucket/path/to/object", dl.bucket, dl.key)
	}
	if dl.endpoint != "https://gcs.example.com" {
		t.Errorf("endpoint = %q, want the host from the URL", dl.endpoint)
	}

	// Google's own hosts keep their existing parse and name no endpoint.
	d, ok = p.DetectForced("https://storage.googleapis.com/my-bucket/key")
	if !ok {
		t.Fatal("DetectForced rejected a Google host")
	}
	if dl := d.(*Downloader); dl.endpoint != "" {
		t.Errorf("endpoint = %q, want empty for a Google host", dl.endpoint)
	}

	if _, ok := p.DetectForced("https://gcs.example.com/"); ok {
		t.Error("DetectForced accepted a URL naming no bucket")
	}
}

func TestPrefix(t *testing.T) {
	p := New()
	if p.Prefix() != "gcs" {
		t.Errorf("Prefix() = %q, want %q", p.Prefix(), "gcs")
	}
}

func TestPriority(t *testing.T) {
	p := New()
	if p.Priority() != 60 {
		t.Errorf("Priority() = %d, want %d", p.Priority(), 60)
	}
}

// stubTokenSource swaps out the Application Default Credentials lookup for the
// duration of a test, so the credentials the machine happens to carry do not
// decide which branch of authOption runs.
func stubTokenSource(t *testing.T, ts oauth2.TokenSource, err error) {
	t.Helper()
	orig := defaultTokenSource
	defaultTokenSource = func(context.Context, ...string) (oauth2.TokenSource, error) {
		return ts, err
	}
	t.Cleanup(func() { defaultTokenSource = orig })
}

// fetchWith downloads an object through a client built from opt, against a
// server that records the Authorization header it was sent. The endpoint is
// passed separately from opt because newService short-circuits to
// WithoutAuthentication whenever an endpoint is configured, which would hide the
// credential-resolution branch under test.
func fetchWith(t *testing.T, opt option.ClientOption) (body, auth string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("chart bytes"))
	}))
	defer srv.Close()

	ctx := context.Background()
	svc, err := storage.NewService(ctx, opt, option.WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	resp, err := svc.Objects.Get("public-bucket", "index.yaml").Context(ctx).Download()
	if err != nil {
		t.Fatalf("downloading object: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading object: %v", err)
	}
	return string(raw), auth
}

// TestAuthOptionNoCredentials covers the fallback taken when no credentials can
// be resolved: the request has to go out unauthenticated, which is what a public
// bucket serves. A token source carrying a nil token satisfies the option's type
// but panics inside the auth transport on the first request - Token.Extra on a
// nil token - taking down anything fetching a public bucket from an environment
// without ADC.
func TestAuthOptionNoCredentials(t *testing.T) {
	stubTokenSource(t, nil, errors.New("could not find default credentials"))

	opt, err := authOption(context.Background(), settings.Settings{}, "")
	if err != nil {
		t.Fatalf("authOption: %v", err)
	}

	body, auth := fetchWith(t, opt)
	if body != "chart bytes" {
		t.Errorf("body = %q, want %q", body, "chart bytes")
	}
	if auth != "" {
		t.Errorf("Authorization = %q, want no credentials to be sent", auth)
	}
}

// TestAuthOptionUsesADC guards the other side of that fallback: when credentials
// do resolve, they are the ones the request carries.
func TestAuthOptionUsesADC(t *testing.T) {
	stubTokenSource(t, oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: "adc-token",
		TokenType:   "Bearer",
	}), nil)

	opt, err := authOption(context.Background(), settings.Settings{}, "")
	if err != nil {
		t.Fatalf("authOption: %v", err)
	}

	if _, auth := fetchWith(t, opt); auth != "Bearer adc-token" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer adc-token")
	}
}

// TestAuthOptionCustomEndpointSkipsADC pins the branch ordering: a custom
// endpoint is an emulator or private endpoint that issues no Google credentials,
// so ADC must not be consulted even when it would resolve.
func TestAuthOptionCustomEndpointSkipsADC(t *testing.T) {
	stubTokenSource(t, oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: "adc-token",
		TokenType:   "Bearer",
	}), nil)

	opt, err := authOption(context.Background(), settings.Settings{}, "http://gcs.internal:4443")
	if err != nil {
		t.Fatalf("authOption: %v", err)
	}

	if _, auth := fetchWith(t, opt); auth != "" {
		t.Errorf("Authorization = %q, want no credentials to be sent", auth)
	}
}
