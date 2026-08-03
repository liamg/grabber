package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/liamg/grabber/settings"
)

// disableCommitSigning pins commit.gpgSign off in the repo's own config.
//
// go-git resolves signing from the merged system/global/local config, so a
// developer (or CI image) with commit.gpgSign enabled in ~/.gitconfig would
// otherwise make these fixtures fail when they build throwaway commits. Grabber
// only ever clones and reads, so no code path under test signs anything.
func disableCommitSigning(t *testing.T, repo *gogit.Repository) {
	t.Helper()

	cfg, err := repo.Config()
	if err != nil {
		t.Fatalf("read repo config: %v", err)
	}
	cfg.Commit.GpgSign = config.NewOptBool(false)
	if err := repo.SetConfig(cfg); err != nil {
		t.Fatalf("write repo config: %v", err)
	}
}

// createBareRepo creates a bare repo with some test files and returns its path.
// The repo will have:
//   - file.txt in root
//   - sub/nested.txt in a subdirectory
//   - a tag "v1.0.0" on the first commit
//   - a second commit on "main" with sub/extra.txt
func createBareRepo(t *testing.T) string {
	t.Helper()

	// Create a non-bare repo first, then clone it as bare.
	workDir := t.TempDir()

	repo, err := gogit.PlainInit(workDir, false)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}
	disableCommitSigning(t, repo)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	// First commit: file.txt and sub/nested.txt
	writeTestFile(t, workDir, "file.txt", "hello")
	writeTestFile(t, workDir, "sub/nested.txt", "nested content")

	if _, err := wt.Add("file.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Add("sub/nested.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}

	sig := &object.Signature{
		Name:  "Test",
		Email: "test@test.com",
		When:  time.Now(),
	}

	commit1, err := wt.Commit("first commit", &gogit.CommitOptions{Author: sig})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Tag the first commit.
	_, err = repo.CreateTag("v1.0.0", commit1, &gogit.CreateTagOptions{
		Tagger:  sig,
		Message: "v1.0.0",
	})
	if err != nil {
		t.Fatalf("tag: %v", err)
	}

	// Second commit: add sub/extra.txt
	writeTestFile(t, workDir, "sub/extra.txt", "extra content")
	if _, err := wt.Add("sub/extra.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("second commit", &gogit.CommitOptions{Author: sig}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Clone as bare repo.
	bareDir := t.TempDir()
	_, err = gogit.PlainClone(bareDir, &gogit.CloneOptions{
		Bare: true,
		URL:  workDir,
	})
	if err != nil {
		t.Fatalf("bare clone: %v", err)
	}

	return bareDir
}

func writeTestFile(t *testing.T, base, rel, content string) {
	t.Helper()
	path := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestDownload_BasicClone(t *testing.T) {
	bareRepo := createBareRepo(t)
	dst := t.TempDir()

	d := &Downloader{repoURL: bareRepo}
	_, err := d.Download(context.Background(), dst, settings.Settings{})
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	assertFileContains(t, filepath.Join(dst, "file.txt"), "hello")
	assertFileContains(t, filepath.Join(dst, "sub/nested.txt"), "nested content")
	assertFileContains(t, filepath.Join(dst, "sub/extra.txt"), "extra content")

	// .git should be removed.
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Error(".git directory should not exist in output")
	}
}

func TestDownload_WithRef_Tag(t *testing.T) {
	bareRepo := createBareRepo(t)
	dst := t.TempDir()

	d := &Downloader{repoURL: bareRepo, ref: "v1.0.0"}
	_, err := d.Download(context.Background(), dst, settings.Settings{})
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	// v1.0.0 was tagged on first commit, before sub/extra.txt was added.
	assertFileContains(t, filepath.Join(dst, "file.txt"), "hello")
	assertFileContains(t, filepath.Join(dst, "sub/nested.txt"), "nested content")
	assertFileNotExists(t, filepath.Join(dst, "sub/extra.txt"))
}

func TestDownload_WithRef_CommitHash(t *testing.T) {
	// Create a repo and get the first commit hash.
	workDir := t.TempDir()
	repo, err := gogit.PlainInit(workDir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	disableCommitSigning(t, repo)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	writeTestFile(t, workDir, "first.txt", "first")
	wt.Add("first.txt")
	sig := &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()}
	hash1, err := wt.Commit("first", &gogit.CommitOptions{Author: sig})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	writeTestFile(t, workDir, "second.txt", "second")
	wt.Add("second.txt")
	wt.Commit("second", &gogit.CommitOptions{Author: sig})

	// Clone as bare.
	bareDir := t.TempDir()
	gogit.PlainClone(bareDir, &gogit.CloneOptions{Bare: true, URL: workDir})

	dst := t.TempDir()
	d := &Downloader{repoURL: bareDir, ref: hash1.String()}
	_, err = d.Download(context.Background(), dst, settings.Settings{})
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	assertFileContains(t, filepath.Join(dst, "first.txt"), "first")
	assertFileNotExists(t, filepath.Join(dst, "second.txt"))
}

func TestDownload_WithRef_ShortCommitHash(t *testing.T) {
	workDir := t.TempDir()
	repo, err := gogit.PlainInit(workDir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	disableCommitSigning(t, repo)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	writeTestFile(t, workDir, "first.txt", "first")
	wt.Add("first.txt")
	sig := &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()}
	hash1, err := wt.Commit("first", &gogit.CommitOptions{Author: sig})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	writeTestFile(t, workDir, "second.txt", "second")
	wt.Add("second.txt")
	wt.Commit("second", &gogit.CommitOptions{Author: sig})

	// Clone as bare.
	bareDir := t.TempDir()
	gogit.PlainClone(bareDir, &gogit.CloneOptions{Bare: true, URL: workDir})

	// Use a 7-char short hash.
	shortHash := hash1.String()[:7]

	dst := t.TempDir()
	d := &Downloader{repoURL: bareDir, ref: shortHash}
	_, err = d.Download(context.Background(), dst, settings.Settings{})
	if err != nil {
		t.Fatalf("download with short hash %q: %v", shortHash, err)
	}

	assertFileContains(t, filepath.Join(dst, "first.txt"), "first")
	assertFileNotExists(t, filepath.Join(dst, "second.txt"))
}

func TestDownload_WithRef_ShortCommitHash_AndSubdir(t *testing.T) {
	workDir := t.TempDir()
	repo, err := gogit.PlainInit(workDir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	disableCommitSigning(t, repo)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	writeTestFile(t, workDir, "root.txt", "root")
	writeTestFile(t, workDir, "sub/inner.txt", "inner-v1")
	wt.Add("root.txt")
	wt.Add("sub/inner.txt")
	sig := &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()}
	hash1, err := wt.Commit("first", &gogit.CommitOptions{Author: sig})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Second commit changes the subdir content.
	writeTestFile(t, workDir, "sub/inner.txt", "inner-v2")
	wt.Add("sub/inner.txt")
	wt.Commit("second", &gogit.CommitOptions{Author: sig})

	bareDir := t.TempDir()
	gogit.PlainClone(bareDir, &gogit.CloneOptions{Bare: true, URL: workDir})

	shortHash := hash1.String()[:8]

	dst := t.TempDir()
	d := &Downloader{repoURL: bareDir, ref: shortHash, subdir: "sub"}
	_, err = d.Download(context.Background(), dst, settings.Settings{})
	if err != nil {
		t.Fatalf("download with short hash + subdir: %v", err)
	}

	// Should have v1 content (from the first commit), at its repo-relative path.
	assertFileContains(t, filepath.Join(dst, "sub", "inner.txt"), "inner-v1")
}

func TestDownload_WithSubdir(t *testing.T) {
	bareRepo := createBareRepo(t)
	dst := t.TempDir()

	d := &Downloader{repoURL: bareRepo, subdir: "sub"}
	_, err := d.Download(context.Background(), dst, settings.Settings{})
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	// The subdir keeps its repository-relative path.
	assertFileContains(t, filepath.Join(dst, "sub", "nested.txt"), "nested content")
	assertFileContains(t, filepath.Join(dst, "sub", "extra.txt"), "extra content")
	assertFileNotExists(t, filepath.Join(dst, "nested.txt"))
}

func TestDownload_WithSubdirAndRef(t *testing.T) {
	bareRepo := createBareRepo(t)
	dst := t.TempDir()

	d := &Downloader{repoURL: bareRepo, subdir: "sub", ref: "v1.0.0"}
	_, err := d.Download(context.Background(), dst, settings.Settings{})
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	// v1.0.0 only has nested.txt in sub/, not extra.txt.
	assertFileContains(t, filepath.Join(dst, "sub", "nested.txt"), "nested content")
	assertFileNotExists(t, filepath.Join(dst, "sub", "extra.txt"))
}

func TestDownload_WithDepth(t *testing.T) {
	bareRepo := createBareRepo(t)
	dst := t.TempDir()

	d := &Downloader{repoURL: bareRepo, depth: 1}
	_, err := d.Download(context.Background(), dst, settings.Settings{})
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	// Shallow clone should still have the latest files.
	assertFileContains(t, filepath.Join(dst, "file.txt"), "hello")
	assertFileContains(t, filepath.Join(dst, "sub/extra.txt"), "extra content")
}

func TestDownload_DepthFromSettings(t *testing.T) {
	bareRepo := createBareRepo(t)
	dst := t.TempDir()

	d := &Downloader{repoURL: bareRepo}
	s := settings.Settings{
		Git: settings.GitConfig{Depth: 1},
	}
	_, err := d.Download(context.Background(), dst, s)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	assertFileContains(t, filepath.Join(dst, "file.txt"), "hello")
}

func TestDownload_URLDepthOverridesSettings(t *testing.T) {
	bareRepo := createBareRepo(t)
	dst := t.TempDir()

	// URL depth=1 should override settings depth=100.
	d := &Downloader{repoURL: bareRepo, depth: 1}
	s := settings.Settings{
		Git: settings.GitConfig{Depth: 100},
	}
	_, err := d.Download(context.Background(), dst, s)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	assertFileContains(t, filepath.Join(dst, "file.txt"), "hello")
}

// TestDownload_PartialCloneFallsBackToFullClone covers a remote that cannot
// serve a partial clone (a local repo, which advertises no filter support).
// The download must still succeed by falling back to a full clone, for every
// subdir spelling — including no subdir at all.
func TestDownload_PartialCloneFallsBackToFullClone(t *testing.T) {
	bareRepo := createBareRepo(t)
	// Partial clones are always attempted; there is no option to enable.
	s := settings.Settings{}

	// With no subdir there is nothing to narrow to, so this is an ordinary full
	// clone (matching go-getter, which gated every sparse flag on a subdir).
	// It must no longer be an error, and must not silently drop subdirectories.
	t.Run("no subdir clones the whole repo", func(t *testing.T) {
		dst := t.TempDir()
		d := &Downloader{repoURL: bareRepo}
		if _, err := d.Download(context.Background(), dst, s); err != nil {
			t.Fatalf("download: %v", err)
		}
		assertFileContains(t, filepath.Join(dst, "file.txt"), "hello")
		assertFileContains(t, filepath.Join(dst, "sub", "nested.txt"), "nested content")
		assertFileContains(t, filepath.Join(dst, "sub", "extra.txt"), "extra content")
	})

	// A subdir keeps its repository-relative path, whichever spelling asked for
	// it, so several subdirs of one repo can be merged into a single tree.
	t.Run("subdir keeps its repository-relative path", func(t *testing.T) {
		dst := t.TempDir()
		d := &Downloader{repoURL: bareRepo, subdir: "sub"}
		if _, err := d.Download(context.Background(), dst, s); err != nil {
			t.Fatalf("download: %v", err)
		}
		assertFileContains(t, filepath.Join(dst, "sub", "nested.txt"), "nested content")
		assertFileNotExists(t, filepath.Join(dst, "nested.txt"))
	})
}

func TestDownload_NonexistentRef(t *testing.T) {
	bareRepo := createBareRepo(t)
	dst := t.TempDir()

	d := &Downloader{repoURL: bareRepo, ref: "nonexistent-branch"}
	_, err := d.Download(context.Background(), dst, settings.Settings{})
	if err == nil {
		t.Fatal("expected error for nonexistent ref")
	}
	if !errors.Is(err, gogit.ErrRemoteRefNotFound) {
		t.Fatalf("expected ErrRemoteRefNotFound, got: %v", err)
	}
	// The error should name the ref the caller pinned, not the internal
	// spelling of the last attempt (refs/tags/nonexistent-branch).
	if !strings.Contains(err.Error(), `"nonexistent-branch"`) {
		t.Errorf("error should name the pinned ref, got: %v", err)
	}
}

func TestDownload_NonexistentRef_SparsePath(t *testing.T) {
	bareRepo := createBareRepo(t)
	dst := t.TempDir()

	// A subdir makes the download sparse-eligible. A missing ref is definitive:
	// it must surface as-is, not as a sparse-unsupported error that triggers a
	// full clone destined to fail the same way.
	d := &Downloader{repoURL: bareRepo, ref: "nonexistent-branch", subdir: "sub"}
	_, err := d.Download(context.Background(), dst, settings.Settings{})
	if err == nil {
		t.Fatal("expected error for nonexistent ref")
	}
	if !errors.Is(err, gogit.ErrRemoteRefNotFound) {
		t.Fatalf("expected ErrRemoteRefNotFound, got: %v", err)
	}
	if strings.Contains(err.Error(), errSparseUnsupported.Error()) {
		t.Errorf("missing ref should not be reported as a sparse-checkout problem, got: %v", err)
	}
}

func TestDownload_NonexistentSubdir(t *testing.T) {
	bareRepo := createBareRepo(t)
	dst := t.TempDir()

	d := &Downloader{repoURL: bareRepo, subdir: "does-not-exist"}
	_, err := d.Download(context.Background(), dst, settings.Settings{})
	if err == nil {
		t.Fatal("expected error for nonexistent subdir")
	}
}

func TestResolveDepth(t *testing.T) {
	tests := []struct {
		name     string
		d        Downloader
		s        settings.Settings
		expected int
	}{
		{
			name:     "url depth takes precedence",
			d:        Downloader{depth: 5},
			s:        settings.Settings{Git: settings.GitConfig{Depth: 10}},
			expected: 5,
		},
		{
			name:     "settings depth used when no url depth",
			d:        Downloader{},
			s:        settings.Settings{Git: settings.GitConfig{Depth: 10}},
			expected: 10,
		},
		{
			name:     "default depth is 1 for branches/tags",
			d:        Downloader{ref: "main"},
			s:        settings.Settings{},
			expected: 1,
		},
		{
			name:     "default depth is 1 when no ref",
			d:        Downloader{},
			s:        settings.Settings{},
			expected: 1,
		},
		{
			name:     "commit hash gets full clone",
			d:        Downloader{ref: "abc1234"},
			s:        settings.Settings{},
			expected: 0,
		},
		{
			name:     "full commit hash gets full clone",
			d:        Downloader{ref: "da39a3ee5e6b4b0d3255bfef95601890afd80709"},
			s:        settings.Settings{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.d.resolveDepth(tt.s)
			if got != tt.expected {
				t.Errorf("resolveDepth() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func assertFileContains(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(data) != expected {
		t.Errorf("%s: got %q, want %q", path, string(data), expected)
	}
}

func assertFileNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to not exist", path)
	}
}
