package s3

import (
	"testing"
)

func TestParseS3URL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantBucket string
		wantKey    string
		wantRegion string
		wantErr    bool
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
			name:    "not an S3 URL",
			url:     "https://example.com/file.txt",
			wantErr: true,
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
			d, err := parseS3URL(tt.url)
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
