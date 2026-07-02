package git

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/liamg/grabber/settings"
)

func TestArchiveURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		ref     string
		want    string
		wantErr bool
	}{
		{
			name: "github with .git suffix",
			url:  "https://github.com/hashicorp/terraform.git",
			ref:  "abc1234",
			want: "https://api.github.com/repos/hashicorp/terraform/tarball/abc1234",
		},
		{
			name: "github without .git suffix",
			url:  "https://github.com/hashicorp/terraform",
			ref:  "abc1234",
			want: "https://api.github.com/repos/hashicorp/terraform/tarball/abc1234",
		},
		{
			name: "gitlab",
			url:  "https://gitlab.com/myorg/myrepo.git",
			ref:  "def5678",
			want: "https://gitlab.com/api/v4/projects/myorg%2Fmyrepo/repository/archive.tar.gz?sha=def5678",
		},
		{
			name: "bitbucket",
			url:  "https://bitbucket.org/myorg/myrepo.git",
			ref:  "aaa1111",
			want: "https://bitbucket.org/myorg/myrepo/get/aaa1111.tar.gz",
		},
		{
			name: "github via https with port",
			url:  "https://github.com:443/hashicorp/terraform.git",
			ref:  "abc1234",
			want: "https://api.github.com/repos/hashicorp/terraform/tarball/abc1234",
		},
		{
			name:    "unsupported host",
			url:     "https://example.com/owner/repo.git",
			ref:     "abc1234",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.url)
			if err != nil {
				t.Fatalf("parse URL %q: %v", tt.url, err)
			}

			got, err := archiveURL(u, tt.ref)
			if (err != nil) != tt.wantErr {
				t.Fatalf("archiveURL() err = %v, wantErr = %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("archiveURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseOwnerRepo(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"with .git suffix", "/hashicorp/terraform.git", "hashicorp", "terraform", false},
		{"without .git suffix", "/hashicorp/terraform", "hashicorp", "terraform", false},
		{"extra segments ignored", "/org/repo/extra/path", "org", "repo", false},
		{"single segment invalid", "/onlyone", "", "", true},
		{"empty path invalid", "", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := parseOwnerRepo(tt.path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseOwnerRepo(%q): err=%v, wantErr=%v", tt.path, err, tt.wantErr)
			}
			if owner != tt.wantOwner || repo != tt.wantRepo {
				t.Errorf("parseOwnerRepo(%q) = (%q, %q), want (%q, %q)", tt.path, owner, repo, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}

func TestTokenFromEnv(t *testing.T) {
	tests := []struct {
		name string
		host string
		vars map[string]string
		want string
	}{
		{"GH_TOKEN", "github.com", map[string]string{"GH_TOKEN": "ghtoken123"}, "ghtoken123"},
		{"GITHUB_TOKEN", "github.com", map[string]string{"GITHUB_TOKEN": "ghtoken456"}, "ghtoken456"},
		{"GH_TOKEN precedence", "github.com", map[string]string{"GH_TOKEN": "primary", "GITHUB_TOKEN": "secondary"}, "primary"},
		{"GITLAB_TOKEN", "gitlab.com", map[string]string{"GITLAB_TOKEN": "gltoken123"}, "gltoken123"},
		{"GL_TOKEN", "gitlab.com", map[string]string{"GL_TOKEN": "gltoken456"}, "gltoken456"},
		{"GITLAB_TOKEN precedence", "gitlab.com", map[string]string{"GITLAB_TOKEN": "primary", "GL_TOKEN": "secondary"}, "primary"},
		{"BITBUCKET_TOKEN", "bitbucket.org", map[string]string{"BITBUCKET_TOKEN": "bbtoken"}, "bbtoken"},
		{"unsupported host", "example.com", map[string]string{"GH_TOKEN": "x"}, ""},
	}

	// The token env vars this function reads. Clear them all around each case
	// so ambient values (e.g. GITHUB_TOKEN in CI) don't leak in.
	allVars := []string{"GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN", "GL_TOKEN", "BITBUCKET_TOKEN"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, v := range allVars {
				t.Setenv(v, "")
			}
			for k, v := range tt.vars {
				t.Setenv(k, v)
			}

			if got := tokenFromEnv(tt.host); got != tt.want {
				t.Errorf("tokenFromEnv(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

// TestFetchArchive_EnvTokenGatedByFallback verifies the archive fallback only
// applies an environment-variable bearer token when the system fallback is on.
func TestFetchArchive_EnvTokenGatedByFallback(t *testing.T) {
	archive := makeArchive(t, "repo", map[string]string{"file.txt": "hi"})

	tests := []struct {
		name             string
		noSystemFallback bool
		wantAuthHeader   string
	}{
		{"fallback on applies token", false, "Bearer envtok"},
		{"fallback off omits token", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GH_TOKEN", "envtok")
			t.Setenv("GITHUB_TOKEN", "")

			var gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/gzip")
				_, _ = w.Write(archive)
			}))
			defer srv.Close()

			archiveURLOverride = srv.URL
			defer func() { archiveURLOverride = "" }()

			d := &Downloader{repoURL: "https://github.com/org/repo.git", ref: "1111111111111111111111111111111111111111"}
			if err := d.fetchArchive(context.Background(), t.TempDir(), settings.Settings{NoSystemFallback: tt.noSystemFallback}); err != nil {
				t.Fatalf("fetchArchive: %v", err)
			}
			if gotAuth != tt.wantAuthHeader {
				t.Errorf("Authorization = %q, want %q", gotAuth, tt.wantAuthHeader)
			}
		})
	}
}

// TestArchiveCredentials_HelperGatedByFallback verifies the system git
// credential helper is only consulted for archive auth when fallback is on.
func TestArchiveCredentials_HelperGatedByFallback(t *testing.T) {
	sentinel := &githttp.BasicAuth{Username: "u", Password: "p"}
	u, _ := url.Parse("https://example.com/org/repo")

	t.Run("consulted when fallback on", func(t *testing.T) {
		called := stubCredentialFill(t, sentinel)
		d := &Downloader{repoURL: u.String()}
		user, pass, ok := d.archiveCredentials(context.Background(), u, settings.Settings{})
		if !ok || user != "u" || pass != "p" {
			t.Fatalf("expected helper creds, got (%q,%q,%v)", user, pass, ok)
		}
		if !*called {
			t.Error("expected the helper to be consulted")
		}
	})

	t.Run("skipped when fallback off", func(t *testing.T) {
		called := stubCredentialFill(t, sentinel)
		d := &Downloader{repoURL: u.String()}
		_, _, ok := d.archiveCredentials(context.Background(), u, settings.Settings{NoSystemFallback: true})
		if ok {
			t.Error("expected no credentials when fallback off")
		}
		if *called {
			t.Error("helper must not be consulted when fallback off")
		}
	})
}

// makeArchive builds a gzip'd tar with a single top-level directory topDir and
// the given files (relative paths under topDir).
func makeArchive(t *testing.T, topDir string, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	// Top-level directory entry.
	if err := tw.WriteHeader(&tar.Header{
		Name:     topDir + "/",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
	}); err != nil {
		t.Fatalf("write dir header: %v", err)
	}

	for rel, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     topDir + "/" + rel,
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Size:     int64(len(content)),
		}); err != nil {
			t.Fatalf("write file header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write file body: %v", err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func TestExtractArchive(t *testing.T) {
	archive := makeArchive(t, "repo-abc123", map[string]string{
		"file.txt":      "hello",
		"sub/inner.txt": "inner",
	})

	t.Run("full extract strips top-level dir", func(t *testing.T) {
		dst := t.TempDir()
		if err := extractArchive(gunzip(t, archive), dst, ""); err != nil {
			t.Fatalf("extractArchive: %v", err)
		}
		assertFileContains(t, filepath.Join(dst, "file.txt"), "hello")
		assertFileContains(t, filepath.Join(dst, "sub/inner.txt"), "inner")
		assertFileNotExists(t, filepath.Join(dst, "repo-abc123"))
	})

	t.Run("subdir extract strips subdir prefix", func(t *testing.T) {
		dst := t.TempDir()
		if err := extractArchive(gunzip(t, archive), dst, "sub"); err != nil {
			t.Fatalf("extractArchive: %v", err)
		}
		assertFileContains(t, filepath.Join(dst, "inner.txt"), "inner")
		assertFileNotExists(t, filepath.Join(dst, "file.txt"))
	})

	t.Run("missing subdir errors", func(t *testing.T) {
		dst := t.TempDir()
		if err := extractArchive(gunzip(t, archive), dst, "nope"); err == nil {
			t.Fatal("expected error for missing subdir")
		}
	})
}

// gunzip decompresses gz and returns a reader over the raw tar stream, matching
// what fetchArchive feeds extractArchive.
func gunzip(t *testing.T, gz []byte) *gzip.Reader {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	return r
}

func TestExtractArchive_RejectsDotDot(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "pwned"
	if err := tw.WriteHeader(&tar.Header{
		Name:     "repo/../../escape.txt",
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()

	dst := t.TempDir()
	if err := extractArchive(gunzip(t, buf.Bytes()), dst, ""); err == nil {
		t.Fatal("expected error for '..' in archive entry")
	}
}

func TestContainsDotDot(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"repo/file.txt", false},
		{"repo/sub/file.txt", false},
		{"repo/../escape", true},
		{"../escape", true},
		{"repo/..", true},
		{"file..txt", false},
		{`repo\..\escape`, true},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := containsDotDot(tt.path); got != tt.want {
				t.Errorf("containsDotDot(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// TestDownload_ArchiveFallback verifies that when git cannot resolve a commit
// hash, the download falls back to the hosting platform's HTTP archive.
func TestDownload_ArchiveFallback(t *testing.T) {
	archive := makeArchive(t, "repo-orphan", map[string]string{
		"file.txt":      "from-archive",
		"sub/inner.txt": "inner-from-archive",
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	archiveURLOverride = srv.URL
	defer func() { archiveURLOverride = "" }()

	bareRepo := createBareRepo(t)

	// A well-formed but nonexistent commit hash: the default-branch clone
	// succeeds, but resolveCommitHash fails, triggering the archive fallback.
	orphanHash := "1111111111111111111111111111111111111111"

	t.Run("full repo", func(t *testing.T) {
		dst := t.TempDir()
		d := &Downloader{repoURL: bareRepo, ref: orphanHash}
		if _, err := d.Download(context.Background(), dst, settings.Settings{}); err != nil {
			t.Fatalf("download: %v", err)
		}
		assertFileContains(t, filepath.Join(dst, "file.txt"), "from-archive")
		assertFileContains(t, filepath.Join(dst, "sub/inner.txt"), "inner-from-archive")
		assertFileNotExists(t, filepath.Join(dst, ".git"))
	})

	t.Run("with subdir", func(t *testing.T) {
		dst := t.TempDir()
		d := &Downloader{repoURL: bareRepo, ref: orphanHash, subdir: "sub"}
		if _, err := d.Download(context.Background(), dst, settings.Settings{}); err != nil {
			t.Fatalf("download: %v", err)
		}
		assertFileContains(t, filepath.Join(dst, "inner.txt"), "inner-from-archive")
		assertFileNotExists(t, filepath.Join(dst, "file.txt"))
	})
}

// TestDownload_ArchiveFallback_NotAttemptedForNamedRef verifies the fallback
// only applies to commit hashes, not branch/tag refs.
func TestDownload_ArchiveFallback_NotAttemptedForNamedRef(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	archiveURLOverride = srv.URL
	defer func() { archiveURLOverride = "" }()

	bareRepo := createBareRepo(t)
	dst := t.TempDir()

	d := &Downloader{repoURL: bareRepo, ref: "nonexistent-branch"}
	if _, err := d.Download(context.Background(), dst, settings.Settings{}); err == nil {
		t.Fatal("expected error for nonexistent named ref")
	}
	if called {
		t.Error("archive fallback should not be attempted for a named ref")
	}
}
