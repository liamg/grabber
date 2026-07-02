package s3

import (
	"context"
	"testing"

	"github.com/liamg/grabber/settings"
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

func TestParseS3URL_Credentials(t *testing.T) {
	t.Run("https form with creds and region", func(t *testing.T) {
		d, err := parseS3URL("https://s3.amazonaws.com/my-bucket/key?aws_access_key_id=AKID&aws_access_key_secret=SECRET&aws_access_token=TOKEN&region=eu-west-1")
		if err != nil {
			t.Fatal(err)
		}
		if d.bucket != "my-bucket" || d.key != "key" {
			t.Errorf("bucket/key = %q/%q, want my-bucket/key", d.bucket, d.key)
		}
		if d.region != "eu-west-1" {
			t.Errorf("region = %q, want eu-west-1", d.region)
		}
		if d.urlCreds == nil {
			t.Fatal("expected urlCreds to be set")
		}
		got, err := d.urlCreds.Retrieve(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.AccessKeyID != "AKID" || got.SecretAccessKey != "SECRET" || got.SessionToken != "TOKEN" {
			t.Errorf("creds = %+v, want AKID/SECRET/TOKEN", got)
		}
	})

	t.Run("s3 scheme form with creds", func(t *testing.T) {
		d, err := parseS3URL("s3://my-bucket/path/file.txt?aws_access_key_id=AKID&aws_access_key_secret=SECRET")
		if err != nil {
			t.Fatal(err)
		}
		if d.bucket != "my-bucket" || d.key != "path/file.txt" {
			t.Errorf("bucket/key = %q/%q, want my-bucket/path/file.txt", d.bucket, d.key)
		}
		if d.urlCreds == nil {
			t.Fatal("expected urlCreds to be set")
		}
	})

	t.Run("region param overrides host-derived region", func(t *testing.T) {
		d, err := parseS3URL("https://s3.us-west-2.amazonaws.com/my-bucket/key?region=eu-west-1")
		if err != nil {
			t.Fatal(err)
		}
		if d.region != "eu-west-1" {
			t.Errorf("region = %q, want eu-west-1 (query overrides host)", d.region)
		}
	})

	t.Run("no credential params leaves urlCreds nil", func(t *testing.T) {
		d, err := parseS3URL("https://s3.amazonaws.com/my-bucket/key?region=us-east-2")
		if err != nil {
			t.Fatal(err)
		}
		if d.urlCreds != nil {
			t.Error("expected urlCreds to be nil when no credential params present")
		}
	})
}

func TestResolveRegion_Precedence(t *testing.T) {
	// URL/host region (stored in d.region) is used when settings has none.
	d := &Downloader{region: "eu-west-1"}
	if got := d.resolveRegion(settings.Settings{}); got != "eu-west-1" {
		t.Errorf("resolveRegion = %q, want eu-west-1", got)
	}
	// settings.AWSCredentials.Region takes precedence.
	s := settings.Settings{AWSCredentials: settings.AWSCredentials{Region: "ap-south-1"}}
	if got := d.resolveRegion(s); got != "ap-south-1" {
		t.Errorf("resolveRegion = %q, want ap-south-1", got)
	}
	// Default when nothing is set.
	empty := &Downloader{}
	if got := empty.resolveRegion(settings.Settings{}); got != "us-east-1" {
		t.Errorf("resolveRegion = %q, want us-east-1", got)
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
