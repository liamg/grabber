package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"

	"github.com/liamg/grabber/settings"
)

// spyPinnedCommitClone records whether the pinned-commit path ran, and whether
// it succeeded, for the duration of the test.
func spyPinnedCommitClone(t *testing.T) (ran, succeeded *bool) {
	t.Helper()
	ran, succeeded = new(bool), new(bool)
	orig := pinnedCommitCloneFunc
	pinnedCommitCloneFunc = func(d *Downloader, ctx context.Context, cloneDir string, s settings.Settings, clientOpts []client.Option) (*gogit.Repository, error) {
		*ran = true
		repo, err := orig(d, ctx, cloneDir, s, clientOpts)
		*succeeded = err == nil
		return repo, err
	}
	t.Cleanup(func() { pinnedCommitCloneFunc = orig })
	return ran, succeeded
}

// createHistoryRepo builds a bare repo whose default branch has three commits,
// plus a side branch that is not on the default branch's history, and returns
// it with the hashes of the first commit and the side branch's tip.
func createHistoryRepo(t *testing.T) (bareDir string, first, side plumbing.Hash) {
	t.Helper()

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
	sig := &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()}
	commit := func(name, content string) plumbing.Hash {
		writeTestFile(t, workDir, name, content)
		if _, err := wt.Add(name); err != nil {
			t.Fatalf("add: %v", err)
		}
		h, err := wt.Commit(name, &gogit.CommitOptions{Author: sig})
		if err != nil {
			t.Fatalf("commit: %v", err)
		}
		return h
	}

	first = commit("first.txt", "first")
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}

	if err := wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("side"),
		Create: true,
	}); err != nil {
		t.Fatalf("checkout side: %v", err)
	}
	side = commit("side.txt", "side")

	if err := wt.Checkout(&gogit.CheckoutOptions{Branch: head.Name()}); err != nil {
		t.Fatalf("checkout default: %v", err)
	}
	commit("second.txt", "second")
	commit("third.txt", "third")

	bareDir = t.TempDir()
	if _, err := gogit.PlainClone(bareDir, &gogit.CloneOptions{Bare: true, URL: workDir}); err != nil {
		t.Fatalf("bare clone: %v", err)
	}
	return bareDir, first, side
}

func TestDownload_PinnedCommit(t *testing.T) {
	bare, first, side := createHistoryRepo(t)

	t.Run("old commit on the default branch", func(t *testing.T) {
		ran, ok := spyPinnedCommitClone(t)
		dst := t.TempDir()
		d := &Downloader{repoURL: bare, ref: first.String()}
		if _, err := d.Download(context.Background(), dst, settings.Settings{}); err != nil {
			t.Fatalf("download: %v", err)
		}
		if !*ran || !*ok {
			t.Fatalf("pinned-commit path ran=%v succeeded=%v, want both", *ran, *ok)
		}
		assertFileContains(t, filepath.Join(dst, "first.txt"), "first")
		assertFileNotExists(t, filepath.Join(dst, "second.txt"))
		assertFileNotExists(t, filepath.Join(dst, "side.txt"))
		assertFileNotExists(t, filepath.Join(dst, ".git"))
	})

	t.Run("commit reachable only from another branch", func(t *testing.T) {
		ran, ok := spyPinnedCommitClone(t)
		dst := t.TempDir()
		d := &Downloader{repoURL: bare, ref: side.String()}
		if _, err := d.Download(context.Background(), dst, settings.Settings{}); err != nil {
			t.Fatalf("download: %v", err)
		}
		if !*ran || !*ok {
			t.Fatalf("pinned-commit path ran=%v succeeded=%v, want both", *ran, *ok)
		}
		assertFileContains(t, filepath.Join(dst, "first.txt"), "first")
		assertFileContains(t, filepath.Join(dst, "side.txt"), "side")
		assertFileNotExists(t, filepath.Join(dst, "second.txt"))
	})

	t.Run("short hash keeps the full clone", func(t *testing.T) {
		ran, _ := spyPinnedCommitClone(t)
		dst := t.TempDir()
		d := &Downloader{repoURL: bare, ref: first.String()[:7]}
		if _, err := d.Download(context.Background(), dst, settings.Settings{}); err != nil {
			t.Fatalf("download: %v", err)
		}
		if *ran {
			t.Fatal("pinned-commit path ran for a short hash")
		}
		assertFileContains(t, filepath.Join(dst, "first.txt"), "first")
	})

	// Over a local path go-git serves the repository itself, and its server
	// answers an unknown want with no packfile rather than "not our ref". That
	// cannot be told apart from a refusal, so it falls back; cloning a local
	// path is cheap. TestDownload_GitDaemon_PinnedCommit covers a real server,
	// which names the missing commit and must not fall back.
	t.Run("unknown commit fails", func(t *testing.T) {
		dst := t.TempDir()
		d := &Downloader{repoURL: bare, ref: "0123456789abcdef0123456789abcdef01234567"}
		_, err := d.Download(context.Background(), dst, settings.Settings{})
		if err == nil {
			t.Fatal("expected an error for a commit that is not on the remote")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Fatalf("error = %v, want it to say the commit was not found", err)
		}
	})
}

// A remote that will not serve a commit by hash must still be cloneable the
// old way.
func TestDownload_PinnedCommitFallsBackToFullClone(t *testing.T) {
	bare, first, _ := createHistoryRepo(t)

	orig := pinnedCommitCloneFunc
	pinnedCommitCloneFunc = func(d *Downloader, ctx context.Context, cloneDir string, s settings.Settings, clientOpts []client.Option) (*gogit.Repository, error) {
		// Leave something behind, so the test also checks the directory is reset
		// before the full clone.
		writeTestFile(t, cloneDir, "stale.txt", "stale")
		return nil, fmt.Errorf("%w: want not allowed", errPinnedFetchUnsupported)
	}
	t.Cleanup(func() { pinnedCommitCloneFunc = orig })

	dst := t.TempDir()
	d := &Downloader{repoURL: bare, ref: first.String()}
	if _, err := d.Download(context.Background(), dst, settings.Settings{}); err != nil {
		t.Fatalf("download: %v", err)
	}
	assertFileContains(t, filepath.Join(dst, "first.txt"), "first")
	assertFileNotExists(t, filepath.Join(dst, "second.txt"))
	assertFileNotExists(t, filepath.Join(dst, "stale.txt"))
}

// Submodules must be initialised against the pinned commit. The fixture's
// first parent commit predates the submodule, and its second adds it.
func TestDownload_PinnedCommitSubmodules(t *testing.T) {
	parent, err := filepath.Abs(filepath.Join("testdata", "submodule", "parent.git"))
	if err != nil {
		t.Fatal(err)
	}
	recurse := settings.Settings{Git: settings.GitConfig{RecurseSubmodules: true}}

	t.Run("commit with the submodule populates it", func(t *testing.T) {
		ran, ok := spyPinnedCommitClone(t)
		dst := t.TempDir()
		d := &Downloader{repoURL: parent, ref: "a9f9f1f3c30fade66e59bbe6852ecc68a46a77ee"}
		if _, err := d.Download(context.Background(), dst, recurse); err != nil {
			t.Fatalf("download: %v", err)
		}
		if !*ran || !*ok {
			t.Fatalf("pinned-commit path ran=%v succeeded=%v, want both", *ran, *ok)
		}
		assertFileContains(t, filepath.Join(dst, "main.txt"), "parent content")
		assertFileContains(t, filepath.Join(dst, "vendor/sub/subfile.txt"), "submodule content")
	})

	t.Run("commit before the submodule has none", func(t *testing.T) {
		dst := t.TempDir()
		d := &Downloader{repoURL: parent, ref: "e6b15f9641a98db1833bfea94844dbadeb7e9026"}
		if _, err := d.Download(context.Background(), dst, recurse); err != nil {
			t.Fatalf("download: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dst, "vendor")); !os.IsNotExist(err) {
			t.Errorf("expected no submodule at the earlier commit, stat err = %v", err)
		}
	})
}

// A rejected credential would be rejected by the full clone too. Falling back
// would offer it twice, so it must be returned for the caller's credential loop
// to try the next one.
func TestDownload_PinnedCommitAuthFailureDoesNotFallBack(t *testing.T) {
	bare, first, _ := createHistoryRepo(t)

	orig := pinnedCommitCloneFunc
	pinnedCommitCloneFunc = func(*Downloader, context.Context, string, settings.Settings, []client.Option) (*gogit.Repository, error) {
		return nil, fmt.Errorf("handshake: %w", transport.ErrAuthenticationRequired)
	}
	t.Cleanup(func() { pinnedCommitCloneFunc = orig })

	dst := t.TempDir()
	d := &Downloader{repoURL: bare, ref: first.String()}
	_, err := d.Download(context.Background(), dst, settings.Settings{})
	if !errors.Is(err, transport.ErrAuthenticationRequired) {
		t.Fatalf("error = %v, want an authentication error", err)
	}
	assertFileNotExists(t, filepath.Join(dst, "first.txt"))
}

// An explicitly requested depth applies to a full hash as it does to any ref.
func TestDownload_PinnedCommitHonoursDepth(t *testing.T) {
	bare, _, _ := createHistoryRepo(t)
	head, err := gogit.PlainOpen(bare)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := head.Head()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		depth int
		s     settings.Settings
		want  int
	}{
		{name: "default", want: 1},
		{name: "url depth", depth: 2, want: 2},
		{name: "settings depth", s: settings.Settings{Git: settings.GitConfig{Depth: 3}}, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var commits int
			orig := pinnedCommitCloneFunc
			pinnedCommitCloneFunc = func(d *Downloader, ctx context.Context, cloneDir string, s settings.Settings, clientOpts []client.Option) (*gogit.Repository, error) {
				repo, err := orig(d, ctx, cloneDir, s, clientOpts)
				if err != nil {
					return nil, err
				}
				iter, err := repo.Log(&gogit.LogOptions{From: ref.Hash()})
				if err != nil {
					return nil, err
				}
				_ = iter.ForEach(func(*object.Commit) error { commits++; return nil })
				return repo, nil
			}
			t.Cleanup(func() { pinnedCommitCloneFunc = orig })

			d := &Downloader{repoURL: bare, ref: ref.Hash().String(), depth: tt.depth}
			if _, err := d.Download(context.Background(), t.TempDir(), tt.s); err != nil {
				t.Fatalf("download: %v", err)
			}
			if commits != tt.want {
				t.Fatalf("fetched %d commits of history, want %d", commits, tt.want)
			}
		})
	}
}

// A pack missing an object the checkout needs must be caught before the
// checkout, so the download can fall back rather than fail.
func TestCommitComplete(t *testing.T) {
	workDir := t.TempDir()
	repo, err := gogit.PlainInit(workDir, false)
	if err != nil {
		t.Fatal(err)
	}
	disableCommitSigning(t, repo)
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, workDir, "a.txt", "a")
	writeTestFile(t, workDir, "sub/b.txt", "b")
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()}
	hash, err := wt.Commit("c", &gogit.CommitOptions{Author: sig})
	if err != nil {
		t.Fatal(err)
	}

	if err := commitComplete(repo, hash); err != nil {
		t.Fatalf("complete commit reported incomplete: %v", err)
	}

	c, err := repo.CommitObject(hash)
	if err != nil {
		t.Fatal(err)
	}
	f, err := c.File("sub/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(workDir, ".git", "objects", f.Hash.String()[:2], f.Hash.String()[2:])); err != nil {
		t.Fatalf("removing blob: %v", err)
	}
	reopened, err := gogit.PlainOpen(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := commitComplete(reopened, hash); err == nil {
		t.Fatal("commit with a missing blob reported complete")
	}
}

// The full clone used when the fetch by hash is unavailable must also
// initialise submodules against the pinned commit, not the default branch.
func TestDownload_PinnedCommitSubmodulesAfterFallback(t *testing.T) {
	parent, err := filepath.Abs(filepath.Join("testdata", "submodule", "parent.git"))
	if err != nil {
		t.Fatal(err)
	}

	orig := pinnedCommitCloneFunc
	pinnedCommitCloneFunc = func(*Downloader, context.Context, string, settings.Settings, []client.Option) (*gogit.Repository, error) {
		return nil, fmt.Errorf("%w: want not allowed", errPinnedFetchUnsupported)
	}
	t.Cleanup(func() { pinnedCommitCloneFunc = orig })

	recurse := settings.Settings{Git: settings.GitConfig{RecurseSubmodules: true}}

	t.Run("commit with the submodule populates it", func(t *testing.T) {
		dst := t.TempDir()
		d := &Downloader{repoURL: parent, ref: "a9f9f1f3c30fade66e59bbe6852ecc68a46a77ee"}
		if _, err := d.Download(context.Background(), dst, recurse); err != nil {
			t.Fatalf("download: %v", err)
		}
		assertFileContains(t, filepath.Join(dst, "vendor/sub/subfile.txt"), "submodule content")
	})

	t.Run("commit before the submodule has none", func(t *testing.T) {
		dst := t.TempDir()
		d := &Downloader{repoURL: parent, ref: "e6b15f9641a98db1833bfea94844dbadeb7e9026"}
		if _, err := d.Download(context.Background(), dst, recurse); err != nil {
			t.Fatalf("download: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dst, "vendor")); !os.IsNotExist(err) {
			t.Errorf("expected no submodule at the earlier commit, stat err = %v", err)
		}
	})
}

// A server that only serves advertised commits may refuse a want by sending
// nothing, which go-git reads as an empty repository. That is a refusal, not
// an answer, so it must fall back like any other.
func TestDownload_PinnedCommitRestrictedEmptyRefusalFallsBack(t *testing.T) {
	bare, first, _ := createHistoryRepo(t)

	orig := fetchObjectsFunc
	fetchObjectsFunc = func(context.Context, *gogit.Repository, string, []plumbing.Hash, packp.Filter, int, []client.Option) error {
		return fmt.Errorf("%w: %w", errWantsRestricted, transport.ErrEmptyRemoteRepository)
	}
	t.Cleanup(func() { fetchObjectsFunc = orig })

	ran, ok := spyPinnedCommitClone(t)
	dst := t.TempDir()
	d := &Downloader{repoURL: bare, ref: first.String()}
	if _, err := d.Download(context.Background(), dst, settings.Settings{}); err != nil {
		t.Fatalf("download: %v", err)
	}
	if !*ran || *ok {
		t.Fatalf("pinned-commit path ran=%v succeeded=%v, want it to run and fall back", *ran, *ok)
	}
	assertFileContains(t, filepath.Join(dst, "first.txt"), "first")
	assertFileNotExists(t, filepath.Join(dst, "second.txt"))
}
