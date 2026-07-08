package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/liamg/grabber/settings"
)

// TestDownload_RecurseSubmodules verifies submodule contents are populated only
// when RecurseSubmodules is enabled — matching `git clone --recursive`.
//
// It clones the committed fixture at testdata/submodule/parent.git, whose
// .gitmodules references the sibling sub.git via the relative URL "../sub.git".
// No git CLI is needed: go-git does the (recursive) clone.
func TestDownload_RecurseSubmodules(t *testing.T) {
	parent, err := filepath.Abs(filepath.Join("testdata", "submodule", "parent.git"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("with recursion populates submodule", func(t *testing.T) {
		dst := t.TempDir()
		d := &Downloader{repoURL: parent}
		if _, err := d.Download(context.Background(), dst, settings.Settings{
			Git: settings.GitConfig{RecurseSubmodules: true},
		}); err != nil {
			t.Fatalf("download: %v", err)
		}
		assertFileContains(t, filepath.Join(dst, "main.txt"), "parent content")
		assertFileContains(t, filepath.Join(dst, "vendor/sub/subfile.txt"), "submodule content")
	})

	t.Run("without recursion leaves submodule empty", func(t *testing.T) {
		dst := t.TempDir()
		d := &Downloader{repoURL: parent}
		if _, err := d.Download(context.Background(), dst, settings.Settings{}); err != nil {
			t.Fatalf("download: %v", err)
		}
		assertFileContains(t, filepath.Join(dst, "main.txt"), "parent content")
		if _, err := os.Stat(filepath.Join(dst, "vendor/sub/subfile.txt")); !os.IsNotExist(err) {
			t.Errorf("expected submodule file absent without recursion, stat err = %v", err)
		}
	})
}
