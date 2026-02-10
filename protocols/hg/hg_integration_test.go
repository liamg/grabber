package hg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/liamg/grabber/settings"
)

func hgAvailable() bool {
	_, err := exec.LookPath("hg")
	return err == nil
}

func initHgRepo(t *testing.T, dir string) {
	t.Helper()

	run := func(args ...string) {
		cmd := exec.Command("hg", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"HGUSER=test <test@test.com>",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("hg %v failed: %s: %v", args, string(out), err)
		}
	}

	run("init")

	// Create some files.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "dir", "nested.txt"), []byte("nested content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run("add", ".")
	run("commit", "-m", "initial commit")

	// Create a tagged revision.
	if err := os.WriteFile(filepath.Join(dir, "v1.txt"), []byte("version 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "v1.txt")
	run("commit", "-m", "add v1")
	run("tag", "-m", "tag v1.0.0", "v1.0.0")
}

func TestIntegration_CloneLocalRepo(t *testing.T) {
	if !hgAvailable() {
		t.Skip("hg not available")
	}

	repoDir := t.TempDir()
	initHgRepo(t, repoDir)

	tmpDir := t.TempDir()
	d := &Downloader{
		repoURL: repoDir,
	}

	_, err := d.Download(context.Background(), tmpDir, settings.Defaults)
	if err != nil {
		t.Fatalf("Download() error: %v", err)
	}

	// Check that hello.txt was downloaded.
	content, err := os.ReadFile(filepath.Join(tmpDir, "hello.txt"))
	if err != nil {
		t.Fatalf("reading hello.txt: %v", err)
	}
	if string(content) != "hello world\n" {
		t.Errorf("hello.txt content = %q, want %q", string(content), "hello world\n")
	}

	// Check nested file.
	content, err = os.ReadFile(filepath.Join(tmpDir, "sub", "dir", "nested.txt"))
	if err != nil {
		t.Fatalf("reading nested.txt: %v", err)
	}
	if string(content) != "nested content\n" {
		t.Errorf("nested.txt content = %q, want %q", string(content), "nested content\n")
	}

	// .hg should be removed.
	if _, err := os.Stat(filepath.Join(tmpDir, ".hg")); !os.IsNotExist(err) {
		t.Error(".hg directory should have been removed")
	}
}

func TestIntegration_CloneWithRev(t *testing.T) {
	if !hgAvailable() {
		t.Skip("hg not available")
	}

	repoDir := t.TempDir()
	initHgRepo(t, repoDir)

	tmpDir := t.TempDir()
	d := &Downloader{
		repoURL: repoDir,
		rev:     "v1.0.0",
	}

	_, err := d.Download(context.Background(), tmpDir, settings.Defaults)
	if err != nil {
		t.Fatalf("Download() error: %v", err)
	}

	// v1.txt should exist at the tagged revision.
	if _, err := os.Stat(filepath.Join(tmpDir, "v1.txt")); os.IsNotExist(err) {
		t.Error("v1.txt should exist at rev v1.0.0")
	}
}

func TestIntegration_CloneWithSubdir(t *testing.T) {
	if !hgAvailable() {
		t.Skip("hg not available")
	}

	repoDir := t.TempDir()
	initHgRepo(t, repoDir)

	tmpDir := t.TempDir()
	d := &Downloader{
		repoURL: repoDir,
		subdir:  "sub/dir",
	}

	_, err := d.Download(context.Background(), tmpDir, settings.Defaults)
	if err != nil {
		t.Fatalf("Download() error: %v", err)
	}

	// Only the subdirectory contents should be present.
	content, err := os.ReadFile(filepath.Join(tmpDir, "nested.txt"))
	if err != nil {
		t.Fatalf("reading nested.txt: %v", err)
	}
	if string(content) != "nested content\n" {
		t.Errorf("nested.txt content = %q, want %q", string(content), "nested content\n")
	}

	// hello.txt should NOT be in tmpDir (it's not in the subdir).
	if _, err := os.Stat(filepath.Join(tmpDir, "hello.txt")); !os.IsNotExist(err) {
		t.Error("hello.txt should not be present when using subdir")
	}
}

func TestIntegration_SparseCheckoutError(t *testing.T) {
	if !hgAvailable() {
		t.Skip("hg not available")
	}

	d := &Downloader{
		repoURL: "/tmp/fake-repo",
	}

	s := settings.Defaults
	s.EnableSparseCheckout = true

	_, err := d.Download(context.Background(), t.TempDir(), s)
	if err == nil {
		t.Fatal("expected error for sparse checkout, got nil")
	}
}

func TestIntegration_HgNotFound(t *testing.T) {
	// Override PATH to simulate hg not being available.
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", "")
	defer os.Setenv("PATH", origPath)

	d := &Downloader{
		repoURL: "/tmp/fake-repo",
	}

	_, err := d.Download(context.Background(), t.TempDir(), settings.Defaults)
	if err == nil {
		t.Fatal("expected error when hg not found, got nil")
	}
	if err.Error() != "hg (Mercurial) executable not found in PATH" {
		t.Errorf("unexpected error: %v", err)
	}
}
