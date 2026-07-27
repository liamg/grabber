package git

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// openFixtureRepo opens the shared test repo and returns it with its HEAD
// commit. The repo is a full clone, so every blob is present and the sparse
// entry/materialise logic can be exercised without a network remote (local
// remotes do not advertise object filters, so the partial-clone path itself
// cannot be driven from a file:// URL).
//
// Layout at HEAD:
//
//	file.txt
//	sub/nested.txt
//	sub/extra.txt
func openFixtureRepo(t *testing.T) *gogit.Repository {
	t.Helper()

	repo, err := gogit.PlainOpen(createBareRepo(t))
	if err != nil {
		t.Fatalf("open fixture repo: %v", err)
	}
	return repo
}

// openNestedFixtureRepo builds a repo with files at several directory depths, so
// cone-mode's "files along the path" rule is observable:
//
//	root.txt
//	a/a.txt
//	a/b/b.txt
//	a/b/c/c.txt
//	other/other.txt
func openNestedFixtureRepo(t *testing.T) *gogit.Repository {
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
	for _, rel := range []string{"root.txt", "a/a.txt", "a/b/b.txt", "a/b/c/c.txt", "other/other.txt"} {
		writeTestFile(t, workDir, rel, "x")
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("add %s: %v", rel, err)
		}
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return repo
}

func entryPaths(entries []sparseEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.path)
	}
	sort.Strings(out)
	return out
}

func assertEqualSlice(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestSparseEntries covers the cone-mode selection rules: which files a subdir
// selects, and where they land.
func TestSparseEntries(t *testing.T) {
	repo := openFixtureRepo(t)

	tests := []struct {
		name   string
		subdir string
		want   []string
	}{
		{
			// Only reachable as a unit: sparseEligible sends a download with no
			// subdir down the full-clone path instead.
			name: "empty subdir selects only the repo root files",
			want: []string{"file.txt"},
		},
		{
			// Everything below the subdir, recursively, plus the root files.
			name:   "subdir keeps repo paths and includes root files",
			subdir: "sub",
			want:   []string{"file.txt", "sub/extra.txt", "sub/nested.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Downloader{subdir: tt.subdir}
			commit, err := d.sparseCommit(repo)
			if err != nil {
				t.Fatalf("sparseCommit: %v", err)
			}
			entries, err := d.sparseEntries(repo, commit)
			if err != nil {
				t.Fatalf("sparseEntries: %v", err)
			}
			assertEqualSlice(t, entryPaths(entries), tt.want)
		})
	}

	t.Run("nested subdir includes the files along the path to it", func(t *testing.T) {
		// Verified against the git binary, which for
		// "git clone --sparse" + "git sparse-checkout set a/b/c" leaves exactly:
		//   root.txt  a/a.txt  a/b/b.txt  a/b/c/c.txt
		// and notably NOT other/other.txt.
		nested := openNestedFixtureRepo(t)
		d := &Downloader{subdir: "a/b/c"}
		commit, err := d.sparseCommit(nested)
		if err != nil {
			t.Fatalf("sparseCommit: %v", err)
		}
		entries, err := d.sparseEntries(nested, commit)
		if err != nil {
			t.Fatalf("sparseEntries: %v", err)
		}
		assertEqualSlice(t, entryPaths(entries),
			[]string{"a/a.txt", "a/b/b.txt", "a/b/c/c.txt", "root.txt"})
	})

	// A subdir pulls in everything beneath it, at any depth — but nothing from
	// sibling branches of the tree.
	t.Run("subdir is recursive", func(t *testing.T) {
		nested := openNestedFixtureRepo(t)
		d := &Downloader{subdir: "a"}
		commit, err := d.sparseCommit(nested)
		if err != nil {
			t.Fatalf("sparseCommit: %v", err)
		}
		entries, err := d.sparseEntries(nested, commit)
		if err != nil {
			t.Fatalf("sparseEntries: %v", err)
		}
		assertEqualSlice(t, entryPaths(entries),
			[]string{"a/a.txt", "a/b/b.txt", "a/b/c/c.txt", "root.txt"})
	})

	t.Run("missing subdir is a hard error, not a fallback", func(t *testing.T) {
		d := &Downloader{subdir: "does-not-exist"}
		commit, err := d.sparseCommit(repo)
		if err != nil {
			t.Fatalf("sparseCommit: %v", err)
		}
		_, err = d.sparseEntries(repo, commit)
		if err == nil {
			t.Fatal("expected an error for a missing subdirectory")
		}
		// A full clone would not find it either, so this must not trigger the
		// fallback path.
		if errors.Is(err, errSparseUnsupported) {
			t.Error("a missing subdirectory must not be reported as unsupported")
		}
	})
}

func TestMaterialise(t *testing.T) {
	repo := openFixtureRepo(t)
	d := &Downloader{subdir: "sub"}

	commit, err := d.sparseCommit(repo)
	if err != nil {
		t.Fatalf("sparseCommit: %v", err)
	}
	entries, err := d.sparseEntries(repo, commit)
	if err != nil {
		t.Fatalf("sparseEntries: %v", err)
	}

	dst := t.TempDir()
	if err := materialise(repo, entries, dst); err != nil {
		t.Fatalf("materialise: %v", err)
	}

	assertFileContains(t, filepath.Join(dst, "file.txt"), "hello")
	assertFileContains(t, filepath.Join(dst, "sub", "nested.txt"), "nested content")
	assertFileContains(t, filepath.Join(dst, "sub", "extra.txt"), "extra content")

	// Nothing but the selected files should exist.
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Error("materialise must not create a .git directory")
	}
}

// TestMaterialiseMissingObjectFallsBack covers the one way go-git's lack of a
// promisor engine (go-git#1381) could still bite: a remote that accepts the
// fetch but does not deliver every object. That must degrade to a full clone,
// not fail the download.
func TestMaterialiseMissingObjectFallsBack(t *testing.T) {
	repo := openFixtureRepo(t)

	missing := []sparseEntry{{
		path: "sub/never-fetched.txt",
		mode: filemode.Regular,
		hash: plumbing.NewHash("0123456789abcdef0123456789abcdef01234567"),
	}}

	err := materialise(repo, missing, t.TempDir())
	if err == nil {
		t.Fatal("expected an error for an undelivered object")
	}
	if !errors.Is(err, errSparseUnsupported) {
		t.Errorf("expected the fallback sentinel so the caller retries with a full clone, got %v", err)
	}
}

func TestSecurePath(t *testing.T) {
	root := t.TempDir()

	t.Run("normal paths resolve under the root", func(t *testing.T) {
		got, err := securePath(root, "sub/file.txt")
		if err != nil {
			t.Fatalf("securePath: %v", err)
		}
		if want := filepath.Join(root, "sub", "file.txt"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	for _, rel := range []string{"../escape.txt", "sub/../../escape.txt"} {
		t.Run("rejects "+rel, func(t *testing.T) {
			if _, err := securePath(root, rel); err == nil {
				t.Errorf("expected %q to be rejected", rel)
			}
		})
	}

	t.Run("absolute paths are clamped under the root", func(t *testing.T) {
		// filepath.Join treats a leading separator as part of the relative path,
		// so this lands inside the destination rather than at the filesystem root.
		got, err := securePath(root, "/etc/passwd")
		if err != nil {
			t.Fatalf("securePath: %v", err)
		}
		if want := filepath.Join(root, "etc", "passwd"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// TestRefPolicy pins the download policy: grabber returns files, never a usable
// repo, so extra branches and tags are downloaded and thrown away. They are
// always off, with a commit hash the sole exception.
func TestRefPolicy(t *testing.T) {
	tests := []struct {
		name             string
		ref              string
		wantSingleBranch bool
		wantTags         plumbing.TagMode
	}{
		{
			name:             "no ref clones only the default branch",
			wantSingleBranch: true,
			wantTags:         plumbing.NoTags,
		},
		{
			name:             "named branch",
			ref:              "main",
			wantSingleBranch: true,
			wantTags:         plumbing.NoTags,
		},
		{
			name:             "named tag",
			ref:              "v1.0.0",
			wantSingleBranch: true,
			wantTags:         plumbing.NoTags,
		},
		{
			// The commit may not be reachable from the default branch tip, and
			// resolving a short hash walks every ref.
			name:             "commit hash keeps all refs",
			ref:              "0123456789abcdef0123456789abcdef01234567",
			wantSingleBranch: false,
			wantTags:         plumbing.AllTags,
		},
		{
			name:             "short commit hash keeps all refs",
			ref:              "0123456",
			wantSingleBranch: false,
			wantTags:         plumbing.AllTags,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Downloader{ref: tt.ref}
			if got := d.tagMode(); got != tt.wantTags {
				t.Errorf("tagMode() = %v, want %v", got, tt.wantTags)
			}
			attempts := d.cloneAttempts()
			if len(attempts) == 0 {
				t.Fatal("expected at least one clone attempt")
			}
			// Every attempt for a given ref shares the same branch scoping.
			for _, a := range attempts {
				if a.singleBranch != tt.wantSingleBranch {
					t.Errorf("singleBranch = %v, want %v (ref %q)",
						a.singleBranch, tt.wantSingleBranch, tt.ref)
				}
			}
		})
	}
}

func TestResolveSubdir(t *testing.T) {
	tests := []struct {
		name        string
		pathSubdir  string
		querySubdir string
		want        string
	}{
		{name: "neither"},
		{
			name:       "// syntax",
			pathSubdir: "modules/vpc",
			want:       "modules/vpc",
		},
		{
			// The same thing, spelled differently.
			name:        "?subdir= syntax",
			querySubdir: "modules/vpc",
			want:        "modules/vpc",
		},
		{
			name:       "// wins over ?subdir=",
			pathSubdir: "from-path",
			// The query form is ignored entirely when both are present.
			querySubdir: "from-query",
			want:        "from-path",
		},
		{
			name:        "cleaned",
			querySubdir: "/modules/../modules/vpc/",
			want:        "modules/vpc",
		},
		{
			name:        "\".\" means the repo root",
			querySubdir: ".",
			want:        "",
		},
		{
			// Traversal is clamped to the repo root rather than escaping it.
			name:        "cannot escape the repo",
			querySubdir: "../../etc",
			want:        "etc",
		},
		{
			name:       "// form is cleaned and clamped too",
			pathSubdir: "../../etc",
			want:       "etc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveSubdir(tt.pathSubdir, tt.querySubdir); got != tt.want {
				t.Errorf("resolveSubdir(%q, %q) = %q, want %q",
					tt.pathSubdir, tt.querySubdir, got, tt.want)
			}
		})
	}
}
