package file

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/liamg/grabber/settings"
)

func TestDownload_AllowedLocalDirectories(t *testing.T) {
	allowed := t.TempDir()
	other := t.TempDir()

	srcInside := filepath.Join(allowed, "ok.txt")
	if err := os.WriteFile(srcInside, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcOutside := filepath.Join(other, "secret.txt")
	if err := os.WriteFile(srcOutside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := settings.Defaults
	s.AllowedLocalDirs = []string{allowed}

	t.Run("a path inside an allowed dir is permitted", func(t *testing.T) {
		d := &Downloader{path: srcInside}
		if _, err := d.Download(context.Background(), t.TempDir(), s); err != nil {
			t.Errorf("expected an allowed path to succeed, got %v", err)
		}
	})

	t.Run("a path outside every allowed dir is rejected", func(t *testing.T) {
		d := &Downloader{path: srcOutside}
		if _, err := d.Download(context.Background(), t.TempDir(), s); err == nil {
			t.Error("expected a path outside the allowed dirs to be rejected")
		}
	})

	t.Run("a symlink inside an allowed dir pointing out is rejected", func(t *testing.T) {
		link := filepath.Join(allowed, "escape")
		if err := os.Symlink(srcOutside, link); err != nil {
			t.Fatal(err)
		}
		d := &Downloader{path: link}
		if _, err := d.Download(context.Background(), t.TempDir(), s); err == nil {
			t.Error("expected a symlink escaping the allowed dir to be rejected")
		}
	})

	t.Run("no restriction by default", func(t *testing.T) {
		d := &Downloader{path: srcOutside}
		if _, err := d.Download(context.Background(), t.TempDir(), settings.Defaults); err != nil {
			t.Errorf("expected no restriction by default, got %v", err)
		}
	})
}
