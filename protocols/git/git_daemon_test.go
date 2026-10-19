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
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
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
