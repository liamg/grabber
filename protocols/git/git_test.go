package git

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"

	httpprotocol "github.com/liamg/grabber/protocols/http"
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
			d, err := parseGitURL(tt.url, false)
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

// TestDetectForced covers the URLs a "git::" prefix has to rescue: the caller
// has said the remote is a Git repository, so neither a missing ".git" suffix
// nor an unrecognised host is grounds for refusing it. Self-hosted instances
// have both, and auto-detection cannot be loosened to accept them because it is
// shared with every other protocol.
func TestDetectForced(t *testing.T) {
	p := New()

	forcedOnly := []string{
		"https://git.selfhosted.example.com/org/repo",
		"https://git.selfhosted.example.com/org/repo//modules/vpc?ref=main",
		"http://git.selfhosted.example.com/org/repo",
	}
	for _, url := range forcedOnly {
		if _, ok := p.Detect(url); ok {
			t.Errorf("Detect(%q) accepted; auto-detection must stay strict", url)
		}
		if _, ok := p.DetectForced(url); !ok {
			t.Errorf("DetectForced(%q) rejected; the prefix commits to git", url)
		}
	}

	// The prefix loosens which URLs are Git, not what a URL means: the ref and
	// subdir still have to come out of it.
	d, ok := p.DetectForced("https://git.selfhosted.example.com/org/repo//modules/vpc?ref=main&depth=1")
	if !ok {
		t.Fatal("expected the forced URL to be accepted")
	}
	dl := d.(*Downloader)
	if dl.repoURL != "https://git.selfhosted.example.com/org/repo" || dl.subdir != "modules/vpc" ||
		dl.ref != "main" || dl.depth != 1 {
		t.Errorf("parsed %+v, want the repo, subdir, ref and depth split out", dl)
	}

	// Nothing that cannot be parsed at all becomes acceptable.
	if _, ok := p.DetectForced("://not a url"); ok {
		t.Error("DetectForced accepted an unparseable URL")
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
		{"gitlab nested groups", "https://gitlab.com/group/subgroup/repo", true},
		{"gitlab with subdir", "https://gitlab.com/group/repo//modules/vpc", true},
		{"azure devops", "https://dev.azure.com/org/project/_git/repo", true},

		// A known host serves plenty that is not a repository. Claiming these
		// clones something that was never a repository, and takes them from the
		// HTTP protocol, which can fetch them.
		{"gitlab module registry download", "https://gitlab.com/api/v4/packages/terraform/modules/v1/ns/name/aws/4.3.0/file?token=secret&archive=tgz", false},
		{"gitlab api without archive", "https://gitlab.com/api/v4/projects/123/repository/files/main.tf", false},
		{"github api path", "https://github.com/api/v3/repos/user/repo", false},
		{"known host, owner only", "https://gitlab.com/user", false},
		{"archive parameter on a known host", "https://gitlab.com/group/repo?archive=tar.gz", false},
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

// TestDetect_ArchiveGoesToHTTP is the whole point of refusing these: a URL the
// Git protocol turns away has to be picked up by the protocol that can fetch
// it, and the Git protocol is the one consulted first.
func TestDetect_ArchiveGoesToHTTP(t *testing.T) {
	const registryDownload = "https://gitlab.com/api/v4/packages/terraform/modules/v1/ns/name/aws/4.3.0/file?token=secret&archive=tgz"

	if _, ok := New().Detect(registryDownload); ok {
		t.Fatalf("Detect(%q) claimed a module registry download", registryDownload)
	}
	if _, ok := httpprotocol.New().Detect(registryDownload); !ok {
		t.Fatalf("the HTTP protocol did not accept %q either; it would now be unfetchable", registryDownload)
	}
	if New().Priority() <= httpprotocol.New().Priority() {
		t.Fatal("the Git protocol no longer outranks HTTP; this test is guarding nothing")
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

func TestCloneAttemptRefNames(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		want []string
	}{
		{
			// A bare name could be either, so both namespaces are tried.
			name: "bare name tries branch then tag",
			ref:  "v1.0.0",
			want: []string{"refs/heads/v1.0.0", "refs/tags/v1.0.0"},
		},
		{
			// The cloudposse convention. Prefixing this would ask for
			// refs/tags/tags/0.19.2, which cannot resolve.
			name: "tags/ is expanded, not prefixed",
			ref:  "tags/0.19.2",
			want: []string{"refs/tags/0.19.2"},
		},
		{
			name: "heads/ is expanded, not prefixed",
			ref:  "heads/main",
			want: []string{"refs/heads/main"},
		},
		{
			name: "fully qualified ref is used as-is",
			ref:  "refs/tags/v1.0.0",
			want: []string{"refs/tags/v1.0.0"},
		},
		{
			name: "fully qualified ref in another namespace is used as-is",
			ref:  "refs/pull/123/head",
			want: []string{"refs/pull/123/head"},
		},
		{
			// A slash is usually just a branch, so heads/ is still tried first.
			name: "slashed branch name still resolves as a branch",
			ref:  "feature/foo",
			want: []string{"refs/heads/feature/foo", "refs/tags/feature/foo", "refs/feature/foo"},
		},
		{
			// git resolves <name> via refs/<name> before the heads and tags
			// namespaces, which is what reaches notes, remotes and pull refs.
			name: "other namespaces fall back to refs/<name>",
			ref:  "notes/commits",
			want: []string{"refs/heads/notes/commits", "refs/tags/notes/commits", "refs/notes/commits"},
		},
		{
			name: "no ref clones the default branch",
			ref:  "",
			want: []string{""},
		},
		{
			name: "commit hash is not a ref name",
			ref:  "da39a3ee5e6b4b0d3255bfef95601890afd80709",
			want: []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Downloader{ref: tt.ref}
			attempts := d.cloneAttempts()

			got := make([]string, 0, len(attempts))
			for _, a := range attempts {
				got = append(got, a.refName.String())
			}

			if len(got) != len(tt.want) {
				t.Fatalf("cloneAttempts() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("attempt %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestQualifiedRefName(t *testing.T) {
	tests := []struct {
		ref    string
		want   string
		wantOK bool
	}{
		{"refs/tags/v1.0.0", "refs/tags/v1.0.0", true},
		{"refs/heads/main", "refs/heads/main", true},
		{"refs/pull/7/head", "refs/pull/7/head", true},
		{"tags/0.19.2", "refs/tags/0.19.2", true},
		{"heads/main", "refs/heads/main", true},
		{"v1.0.0", "", false},
		{"main", "", false},
		{"feature/foo", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got, ok := qualifiedRefName(tt.ref)
			if ok != tt.wantOK {
				t.Fatalf("qualifiedRefName(%q) ok = %v, want %v", tt.ref, ok, tt.wantOK)
			}
			if got.String() != tt.want {
				t.Errorf("qualifiedRefName(%q) = %q, want %q", tt.ref, got, tt.want)
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
	disableCommitSigning(t, repo)

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
		{
			// Azure DevOps serves HTTPS from a different host and path layout,
			// so a scheme swap alone would leave a host that has no Git over
			// HTTP. This is the inverse of the case in httpsToSSH.
			name: "azure devops ssh scheme",
			url:  "ssh://git@ssh.dev.azure.com/v3/org/proj/repo",
			want: "https://dev.azure.com/org/proj/_git/repo",
		},
		{
			name: "azure devops scp style",
			url:  "git@ssh.dev.azure.com:v3/org/proj/repo",
			want: "https://dev.azure.com/org/proj/_git/repo",
		},
		{
			name: "azure devops without the v3 prefix is left alone",
			url:  "ssh://git@ssh.dev.azure.com/org/proj/repo",
			want: "https://ssh.dev.azure.com/org/proj/repo",
		},
		{
			name: "azure devops with too few path segments is left alone",
			url:  "ssh://git@ssh.dev.azure.com/v3/org/proj",
			want: "https://ssh.dev.azure.com/v3/org/proj",
		},
		{
			name: "azure devops https form is already correct",
			url:  "https://dev.azure.com/org/proj/_git/repo",
			want: "https://dev.azure.com/org/proj/_git/repo",
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
