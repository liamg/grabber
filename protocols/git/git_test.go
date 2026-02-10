package git

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestParseGitURL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantRepo   string
		wantRef    string
		wantSubdir string
		wantDepth  int
		wantErr    bool
	}{
		{
			name:     "https github",
			url:      "https://github.com/user/repo.git",
			wantRepo: "https://github.com/user/repo.git",
		},
		{
			name:     "https github without .git",
			url:      "https://github.com/user/repo",
			wantRepo: "https://github.com/user/repo",
		},
		{
			name:     "https with ref",
			url:      "https://github.com/user/repo.git?ref=v1.0.0",
			wantRepo: "https://github.com/user/repo.git",
			wantRef:  "v1.0.0",
		},
		{
			name:      "https with depth",
			url:       "https://github.com/user/repo.git?depth=1",
			wantRepo:  "https://github.com/user/repo.git",
			wantDepth: 1,
		},
		{
			name:      "https with ref and depth",
			url:       "https://github.com/user/repo.git?ref=main&depth=5",
			wantRepo:  "https://github.com/user/repo.git",
			wantRef:   "main",
			wantDepth: 5,
		},
		{
			name:       "https with subdir",
			url:        "https://github.com/user/repo.git//sub/dir",
			wantRepo:   "https://github.com/user/repo.git",
			wantSubdir: "sub/dir",
		},
		{
			name:       "https with subdir and ref",
			url:        "https://github.com/user/repo.git//sub/dir?ref=v2.0.0",
			wantRepo:   "https://github.com/user/repo.git",
			wantSubdir: "sub/dir",
			wantRef:    "v2.0.0",
		},
		{
			name:     "ssh scheme",
			url:      "ssh://git@github.com/user/repo.git",
			wantRepo: "ssh://git@github.com/user/repo.git",
		},
		{
			name:     "scp style",
			url:      "git@github.com:user/repo.git",
			wantRepo: "git@github.com:user/repo.git",
		},
		{
			name:     "scp style with ref",
			url:      "git@github.com:user/repo.git?ref=develop",
			wantRepo: "git@github.com:user/repo.git",
			wantRef:  "develop",
		},
		{
			name:       "scp style with subdir",
			url:        "git@github.com:user/repo.git//modules/vpc",
			wantRepo:   "git@github.com:user/repo.git",
			wantSubdir: "modules/vpc",
		},
		{
			name:     "no scheme known host",
			url:      "github.com/user/repo",
			wantRepo: "https://github.com/user/repo",
		},
		{
			name:     "gitlab",
			url:      "https://gitlab.com/user/repo.git",
			wantRepo: "https://gitlab.com/user/repo.git",
		},
		{
			name:     "bitbucket",
			url:      "https://bitbucket.org/user/repo.git",
			wantRepo: "https://bitbucket.org/user/repo.git",
		},
		{
			name:     "azure devops",
			url:      "https://dev.azure.com/org/project/_git/repo",
			wantRepo: "https://dev.azure.com/org/project/_git/repo",
		},
		{
			name:    "not a git URL",
			url:     "https://example.com/file.txt",
			wantErr: true,
		},
		{
			name:      "negative depth ignored",
			url:       "https://github.com/user/repo.git?depth=-1",
			wantRepo:  "https://github.com/user/repo.git",
			wantDepth: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := parseGitURL(tt.url)
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
			if d.ref != tt.wantRef {
				t.Errorf("ref = %q, want %q", d.ref, tt.wantRef)
			}
			if d.subdir != tt.wantSubdir {
				t.Errorf("subdir = %q, want %q", d.subdir, tt.wantSubdir)
			}
			if d.depth != tt.wantDepth {
				t.Errorf("depth = %d, want %d", d.depth, tt.wantDepth)
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
		{"github https", "https://github.com/user/repo.git", true},
		{"github no .git", "https://github.com/user/repo", true},
		{"gitlab", "https://gitlab.com/user/repo", true},
		{"scp style", "git@github.com:user/repo.git", true},
		{"ssh scheme", "ssh://git@github.com/user/repo.git", true},
		{"not git", "https://example.com/file.txt", false},
		{"s3 url", "https://s3.amazonaws.com/bucket/key", false},
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

func TestLooksLikeCommitHash(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"abc1234", true},
		{"da39a3ee5e6b4b0d3255bfef95601890afd80709", true},
		{"main", false},
		{"v1.0.0", false},
		{"abc12", false},     // too short
		{"xyz123456", false}, // non-hex
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got := looksLikeCommitHash(tt.ref)
			if got != tt.want {
				t.Errorf("looksLikeCommitHash(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestResolveCommitHash(t *testing.T) {
	// Create a repo with two commits.
	workDir := t.TempDir()
	repo, err := git.PlainInit(workDir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt.Add("a.txt")
	hash1, err := wt.Commit("first", &git.CommitOptions{
		Author: &object.Signature{Name: "T", Email: "t@t", When: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(workDir, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt.Add("b.txt")
	hash2, err := wt.Commit("second", &git.CommitOptions{
		Author: &object.Signature{Name: "T", Email: "t@t", When: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Full hash resolves.
	got, err := resolveCommitHash(repo, hash1.String())
	if err != nil {
		t.Fatalf("full hash: %v", err)
	}
	if got != hash1 {
		t.Errorf("full hash: got %s, want %s", got, hash1)
	}

	// Short hash resolves.
	got, err = resolveCommitHash(repo, hash2.String()[:7])
	if err != nil {
		t.Fatalf("short hash: %v", err)
	}
	if got != hash2 {
		t.Errorf("short hash: got %s, want %s", got, hash2)
	}

	// Nonexistent hash fails.
	_, err = resolveCommitHash(repo, "0000000000000000000000000000000000000000")
	if err == nil {
		t.Error("expected error for nonexistent hash")
	}

	// Nonexistent short hash fails.
	_, err = resolveCommitHash(repo, "0000000")
	if err == nil {
		t.Error("expected error for nonexistent short hash")
	}
}

func TestSSHToHTTPS(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "scp style",
			url:  "git@github.com:user/repo.git",
			want: "https://github.com/user/repo.git",
		},
		{
			name: "scp style without .git",
			url:  "git@github.com:user/repo",
			want: "https://github.com/user/repo",
		},
		{
			name: "scp style gitlab",
			url:  "git@gitlab.com:org/project.git",
			want: "https://gitlab.com/org/project.git",
		},
		{
			name: "ssh scheme",
			url:  "ssh://git@github.com/user/repo.git",
			want: "https://github.com/user/repo.git",
		},
		{
			name: "ssh scheme with port",
			url:  "ssh://git@github.com:22/user/repo.git",
			want: "https://github.com:22/user/repo.git",
		},
		{
			name: "already https",
			url:  "https://github.com/user/repo.git",
			want: "https://github.com/user/repo.git",
		},
		{
			name: "already http",
			url:  "http://github.com/user/repo.git",
			want: "http://github.com/user/repo.git",
		},
		{
			name: "git scheme unchanged",
			url:  "git://github.com/user/repo.git",
			want: "git://github.com/user/repo.git",
		},
		{
			name: "scp with custom user",
			url:  "deploy@example.com:org/repo.git",
			want: "https://example.com/org/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sshToHTTPS(tt.url)
			if got != tt.want {
				t.Errorf("sshToHTTPS(%q) = %q, want %q", tt.url, got, tt.want)
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
		{"/user/repo.git//sub/dir", "/user/repo.git", "sub/dir"},
		{"/user/repo.git", "/user/repo.git", ""},
		{"/user/repo.git//", "/user/repo.git", ""},
		{"git@github.com:user/repo.git//modules/vpc", "git@github.com:user/repo.git", "modules/vpc"},
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
