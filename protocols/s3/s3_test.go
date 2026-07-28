package s3

import (
	"testing"
)

func TestParseS3URL(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		allowCustom  bool
		wantBucket   string
		wantKey      string
		wantRegion   string
		wantEndpoint string
		wantErr      bool
	}{
		{
			name:       "path-style global endpoint",
			url:        "https://s3.amazonaws.com/my-bucket/path/to/file.txt",
			wantBucket: "my-bucket",
			wantKey:    "path/to/file.txt",
		},
		{
			name:       "path-style regional endpoint",
			url:        "https://s3.us-west-2.amazonaws.com/my-bucket/path/to/file.txt",
			wantBucket: "my-bucket",
			wantKey:    "path/to/file.txt",
			wantRegion: "us-west-2",
		},
		{
			name:       "virtual-hosted style global endpoint",
			url:        "https://my-bucket.s3.amazonaws.com/path/to/file.txt",
			wantBucket: "my-bucket",
			wantKey:    "path/to/file.txt",
		},
		{
			name:       "virtual-hosted style regional endpoint",
			url:        "https://my-bucket.s3.us-west-2.amazonaws.com/path/to/file.txt",
			wantBucket: "my-bucket",
			wantKey:    "path/to/file.txt",
			wantRegion: "us-west-2",
		},
		{
			name:       "no scheme",
			url:        "s3.amazonaws.com/my-bucket/file.txt",
			wantBucket: "my-bucket",
			wantKey:    "file.txt",
		},
		{
			name:       "bucket only path-style",
			url:        "https://s3.amazonaws.com/my-bucket",
			wantBucket: "my-bucket",
		},
		{
			name:       "directory key with trailing slash",
			url:        "https://s3.amazonaws.com/my-bucket/some/dir/",
			wantBucket: "my-bucket",
			wantKey:    "some/dir/",
		},
		{
			name:       "s3 scheme with bucket and key",
			url:        "s3://my-bucket/path/to/file.txt",
			wantBucket: "my-bucket",
			wantKey:    "path/to/file.txt",
		},
		{
			name:       "s3 scheme bucket only",
			url:        "s3://my-bucket",
			wantBucket: "my-bucket",
		},
		{
			name:       "s3 scheme with trailing slash (directory)",
			url:        "s3://my-bucket/some/dir/",
			wantBucket: "my-bucket",
			wantKey:    "some/dir/",
		},
		{
			name:    "s3 scheme empty",
			url:     "s3://",
			wantErr: true,
		},
		{
			name:    "not an S3 URL",
			url:     "https://example.com/file.txt",
			wantErr: true,
		},
		{
			name:    "custom endpoint rejected when not allowed",
			url:     "https://minio.internal/my-bucket/path/to/file.txt",
			wantErr: true,
		},
		{
			name:         "custom endpoint allowed (forced)",
			url:          "https://minio.internal/my-bucket/path/to/file.txt",
			allowCustom:  true,
			wantBucket:   "my-bucket",
			wantKey:      "path/to/file.txt",
			wantRegion:   "us-east-1",
			wantEndpoint: "https://minio.internal",
		},
		{
			name:         "custom endpoint with explicit region",
			url:          "https://nyc3.digitaloceanspaces.com/my-space/dir/obj?region=nyc3",
			allowCustom:  true,
			wantBucket:   "my-space",
			wantKey:      "dir/obj",
			wantRegion:   "nyc3",
			wantEndpoint: "https://nyc3.digitaloceanspaces.com",
		},
		{
			name:         "custom endpoint bucket only",
			url:          "https://minio.internal/my-bucket",
			allowCustom:  true,
			wantBucket:   "my-bucket",
			wantRegion:   "us-east-1",
			wantEndpoint: "https://minio.internal",
		},
		{
			name:        "custom endpoint no bucket",
			url:         "https://minio.internal/",
			allowCustom: true,
			wantErr:     true,
		},
		{
			name:         "amazonaws still virtual-hosted when custom allowed",
			url:          "https://my-bucket.s3.us-west-2.amazonaws.com/path/to/file.txt",
			allowCustom:  true,
			wantBucket:   "my-bucket",
			wantKey:      "path/to/file.txt",
			wantRegion:   "us-west-2",
			wantEndpoint: "",
		},
		{
			name:    "no bucket in path-style",
			url:     "https://s3.amazonaws.com/",
			wantErr: true,
		},
		{
			name:    "no bucket in path-style no trailing slash",
			url:     "https://s3.amazonaws.com",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := parseS3URL(tt.url, tt.allowCustom)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d.bucket != tt.wantBucket {
				t.Errorf("bucket = %q, want %q", d.bucket, tt.wantBucket)
			}
			if d.key != tt.wantKey {
				t.Errorf("key = %q, want %q", d.key, tt.wantKey)
			}
			if d.region != tt.wantRegion {
				t.Errorf("region = %q, want %q", d.region, tt.wantRegion)
			}
			if d.endpoint != tt.wantEndpoint {
				t.Errorf("endpoint = %q, want %q", d.endpoint, tt.wantEndpoint)
			}
		})
	}
}

func TestDetect(t *testing.T) {
	p := New()

	tests := []struct {
		name   string
		url    string
		wantOK bool
	}{
		{"s3 path-style", "https://s3.amazonaws.com/bucket/key", true},
		{"s3 virtual-hosted", "https://bucket.s3.amazonaws.com/key", true},
		{"s3 scheme", "s3://my-bucket/key", true},
		{"not s3", "https://example.com/file.txt", false},
		{"git url", "https://github.com/user/repo.git", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := p.Detect(tt.url)
			if ok != tt.wantOK {
				t.Errorf("Detect() ok = %v, want %v", ok, tt.wantOK)
			}
		})
	}
}

// TestDetectForced proves a custom S3-compatible endpoint is accepted only via
// the forced path (DetectForced), never by strict auto-detection (Detect) —
// otherwise a bare https:// URL could be hijacked from the HTTP protocol.
func TestDetectForced(t *testing.T) {
	p := New()

	tests := []struct {
		name       string
		url        string
		wantAuto   bool // Detect (strict, shared auto-detection)
		wantForced bool // DetectForced (s3:: committed)
	}{
		{"aws host both", "https://s3.amazonaws.com/bucket/key", true, true},
		{"s3 scheme both", "s3://bucket/key", true, true},
		{"custom endpoint forced only", "https://minio.internal/bucket/key", false, true},
		// Forcing s3:: on any https URL commits to S3 (first path segment as
		// bucket); the point is that strict auto-detection must NOT match it.
		{"plain https matches only when forced", "https://example.com/archive.tar.gz", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := p.Detect(tt.url); ok != tt.wantAuto {
				t.Errorf("Detect() ok = %v, want %v", ok, tt.wantAuto)
			}
			if _, ok := p.DetectForced(tt.url); ok != tt.wantForced {
				t.Errorf("DetectForced() ok = %v, want %v", ok, tt.wantForced)
			}
		})
	}
}
