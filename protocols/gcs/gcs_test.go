package gcs

import (
	"testing"
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
			d, err := parseGCSURL(tt.url)
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
