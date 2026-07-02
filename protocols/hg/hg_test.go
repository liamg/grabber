package hg

import (
	"context"
	"strings"
	"testing"

	"github.com/liamg/grabber/settings"
)

func TestDownload_NoSystemFallbackDisablesHg(t *testing.T) {
	d := &Downloader{repoURL: "https://bitbucket.org/user/repo"}
	_, err := d.Download(context.Background(), t.TempDir(), settings.Settings{NoSystemFallback: true})
	if err == nil {
		t.Fatal("expected an error when system fallback is disabled")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("expected a 'disabled' error, got %v", err)
	}
}

func TestParseHgURL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantRepo   string
		wantRev    string
		wantSubdir string
		wantErr    bool
	}{
		{
			name:     "bitbucket https",
			url:      "https://bitbucket.org/user/repo",
			wantRepo: "https://bitbucket.org/user/repo",
		},
		{
			name:     "bitbucket no scheme",
			url:      "bitbucket.org/user/repo",
			wantRepo: "https://bitbucket.org/user/repo",
		},
		{
			name:     "bitbucket with rev",
			url:      "https://bitbucket.org/user/repo?rev=v1.0.0",
			wantRepo: "https://bitbucket.org/user/repo",
			wantRev:  "v1.0.0",
		},
		{
			name:       "bitbucket with subdir",
			url:        "https://bitbucket.org/user/repo//sub/dir",
			wantRepo:   "https://bitbucket.org/user/repo",
			wantSubdir: "sub/dir",
		},
		{
			name:       "bitbucket with subdir and rev",
			url:        "https://bitbucket.org/user/repo//lib/core?rev=stable",
			wantRepo:   "https://bitbucket.org/user/repo",
			wantSubdir: "lib/core",
			wantRev:    "stable",
		},
		{
			name:     "bitbucket with rev as branch name",
			url:      "https://bitbucket.org/user/repo?rev=feature-branch",
			wantRepo: "https://bitbucket.org/user/repo",
			wantRev:  "feature-branch",
		},
		{
			name:    "not a mercurial URL",
			url:     "https://example.com/file.txt",
			wantErr: true,
		},
		{
			name:    "github is not mercurial",
			url:     "https://github.com/user/repo",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := parseHgURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d.repoURL != tt.wantRepo {
				t.Errorf("repoURL = %q, want %q", d.repoURL, tt.wantRepo)
			}
			if d.rev != tt.wantRev {
				t.Errorf("rev = %q, want %q", d.rev, tt.wantRev)
			}
			if d.subdir != tt.wantSubdir {
				t.Errorf("subdir = %q, want %q", d.subdir, tt.wantSubdir)
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
		{"bitbucket https", "https://bitbucket.org/user/repo", true},
		{"bitbucket no scheme", "bitbucket.org/user/repo", true},
		{"github is not hg", "https://github.com/user/repo", false},
		{"s3 url", "https://s3.amazonaws.com/bucket/key", false},
		{"random url", "https://example.com/file.txt", false},
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

func TestSplitSubdir(t *testing.T) {
	tests := []struct {
		path       string
		wantRepo   string
		wantSubdir string
	}{
		{"/user/repo//sub/dir", "/user/repo", "sub/dir"},
		{"/user/repo", "/user/repo", ""},
		{"/user/repo//", "/user/repo", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			repo, subdir := splitSubdir(tt.path)
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
			if subdir != tt.wantSubdir {
				t.Errorf("subdir = %q, want %q", subdir, tt.wantSubdir)
			}
		})
	}
}

func TestProtocolProperties(t *testing.T) {
	p := New()

	if p.Prefix() != "hg" {
		t.Errorf("Prefix() = %q, want %q", p.Prefix(), "hg")
	}

	if p.Priority() != 80 {
		t.Errorf("Priority() = %d, want %d", p.Priority(), 80)
	}
}
