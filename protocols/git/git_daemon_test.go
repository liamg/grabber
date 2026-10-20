package git

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/protocol"

	"github.com/liamg/grabber/settings"
	"github.com/liamg/grabber/ssrf"
)

// startGitDaemon serves bareRepo over the git:// protocol with "git daemon",
// using git's defaults. Those defaults matter here: over protocol v0 the daemon
// refuses a want for any commit that is not a ref tip, which is how a plain,
// self-hosted git server behaves.
func startGitDaemon(t *testing.T, bareRepo string) string {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping git daemon test in short mode")
	}

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not found; skipping git daemon test")
	}

	root := t.TempDir()
	if err := os.Rename(bareRepo, filepath.Join(root, "repo.git")); err != nil {
		t.Fatalf("moving bare repo: %v", err)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	cmd := exec.Command(gitBin, "daemon",
		"--base-path="+root, "--export-all", "--reuseaddr",
		"--listen=127.0.0.1", fmt.Sprintf("--port=%d", port), root)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting git daemon: %v", err)
	}
	// The port is free between closing the listener above and the daemon
	// binding it. If something else takes it, the daemon exits; watch for that
	// so the test fails at once instead of talking to whatever holds the port.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		select {
		case err := <-exited:
			exited <- err
			t.Fatalf("git daemon exited before it was ready: %v", err)
		default:
		}
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("git daemon did not start: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	return "git://" + addr + "/repo.git"
}

// A pinned commit that is not a ref tip is never advertised. A plain git server
// only serves such a commit by hash over protocol v2, so it must be requested,
// or the download falls back to cloning every ref.
func TestDownload_GitDaemon_PinnedCommit(t *testing.T) {
	bare, first, _ := createHistoryRepo(t)
	repoURL := startGitDaemon(t, bare)
	s := settings.Settings{SSRFLevel: ssrf.None}

	t.Run("unadvertised commit is fetched by hash", func(t *testing.T) {
		ran, ok := spyPinnedCommitClone(t)
		dst := t.TempDir()
		d := &Downloader{repoURL: repoURL, ref: first.String()}
		if _, err := d.Download(context.Background(), dst, s); err != nil {
			t.Fatalf("download: %v", err)
		}
		if !*ran || !*ok {
			t.Fatalf("pinned-commit path ran=%v succeeded=%v, want both", *ran, *ok)
		}
		assertFileContains(t, filepath.Join(dst, "first.txt"), "first")
		assertFileNotExists(t, filepath.Join(dst, "second.txt"))
	})

	t.Run("unknown commit is reported as missing without a full clone", func(t *testing.T) {
		dst := t.TempDir()
		d := &Downloader{repoURL: repoURL, ref: "0123456789abcdef0123456789abcdef01234567"}
		_, err := d.Download(context.Background(), dst, s)
		if err == nil {
			t.Fatal("expected an error for a commit that is not on the remote")
		}
		if !errors.Is(err, gogit.ErrRemoteRefNotFound) {
			t.Fatalf("error = %v, want it to wrap %v", err, gogit.ErrRemoteRefNotFound)
		}
	})
}

// A server that only speaks protocol v0 refuses a want for any commit that is
// not a ref tip, whether or not it has it. That refusal must fall back to the
// full clone, not be read as the commit being missing.
func TestDownload_GitDaemon_PinnedCommitOverV0(t *testing.T) {
	bare, first, _ := createHistoryRepo(t)
	repoURL := startGitDaemon(t, bare)
	s := settings.Settings{SSRFLevel: ssrf.None}

	orig := fetchProtocol
	fetchProtocol = protocol.V0
	t.Cleanup(func() { fetchProtocol = orig })

	ran, ok := spyPinnedCommitClone(t)
	dst := t.TempDir()
	d := &Downloader{repoURL: repoURL, ref: first.String()}
	if _, err := d.Download(context.Background(), dst, s); err != nil {
		t.Fatalf("download: %v", err)
	}
	if !*ran || *ok {
		t.Fatalf("pinned-commit path ran=%v succeeded=%v, want it to run and fall back", *ran, *ok)
	}
	assertFileContains(t, filepath.Join(dst, "first.txt"), "first")
	assertFileNotExists(t, filepath.Join(dst, "second.txt"))
}

// createSubdirHistoryRepo builds a bare repo, configured to serve partial
// clones, whose default branch has two commits under sub/, and returns it with
// the hash of the first. Only the second is a ref tip.
func createSubdirHistoryRepo(t *testing.T) (bareDir string, first plumbing.Hash) {
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
	first = commit("sub/first.txt", "first")
	commit("sub/second.txt", "second")

	bareDir = t.TempDir()
	if _, err := gogit.PlainClone(bareDir, &gogit.CloneOptions{Bare: true, URL: workDir}); err != nil {
		t.Fatalf("bare clone: %v", err)
	}
	if out, err := exec.Command("git", "-C", bareDir, "config", "uploadpack.allowFilter", "true").CombinedOutput(); err != nil {
		t.Fatalf("enabling filters: %v: %s", err, out)
	}
	return bareDir, first
}

// The sparse path clones only the branch tip, so a pinned older commit has to
// be fetched by hash before its tree can be walked.
func TestDownload_GitDaemon_SparsePinnedCommit(t *testing.T) {
	bare, first := createSubdirHistoryRepo(t)
	repoURL := startGitDaemon(t, bare)

	ran, _ := spyPinnedCommitClone(t)
	dst := t.TempDir()
	d := &Downloader{repoURL: repoURL, ref: first.String(), subdir: "sub"}
	if _, err := d.Download(context.Background(), dst, settings.Settings{SSRFLevel: ssrf.None}); err != nil {
		t.Fatalf("download: %v", err)
	}
	if *ran {
		t.Fatal("sparse checkout fell back to a full clone")
	}
	assertFileContains(t, filepath.Join(dst, "sub", "first.txt"), "first")
	assertFileNotExists(t, filepath.Join(dst, "sub", "second.txt"))
}

// A v0 server that serves partial clones but only advertised commits refuses
// the sparse path's fetch by hash. That refusal must fall back to a clone that
// can find the commit, not fail the download.
func TestDownload_GitDaemon_SparsePinnedCommitOverV0(t *testing.T) {
	bare, first := createSubdirHistoryRepo(t)
	repoURL := startGitDaemon(t, bare)

	orig := fetchProtocol
	fetchProtocol = protocol.V0
	t.Cleanup(func() { fetchProtocol = orig })

	ran, _ := spyPinnedCommitClone(t)
	dst := t.TempDir()
	d := &Downloader{repoURL: repoURL, ref: first.String(), subdir: "sub"}
	if _, err := d.Download(context.Background(), dst, settings.Settings{SSRFLevel: ssrf.None}); err != nil {
		t.Fatalf("download: %v", err)
	}
	if !*ran {
		t.Fatal("sparse checkout did not fall back")
	}
	assertFileContains(t, filepath.Join(dst, "sub", "first.txt"), "first")
	assertFileNotExists(t, filepath.Join(dst, "sub", "second.txt"))
}
