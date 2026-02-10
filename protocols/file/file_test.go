package file

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/liamg/grabber/settings"
)

func TestDetect(t *testing.T) {
	p := New()

	tests := []struct {
		name  string
		url   string
		match bool
	}{
		{"file scheme", "file:///tmp/test", true},
		{"absolute path", "/tmp/test", true},
		{"relative path", "relative/path", false},
		{"https url", "https://example.com/file", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := p.Detect(tt.url)
			if ok != tt.match {
				t.Errorf("Detect(%q) = %v, want %v", tt.url, ok, tt.match)
			}
		})
	}
}

func TestParseFileURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantPath string
		wantErr  bool
	}{
		{"file scheme", "file:///tmp/test", "/tmp/test", false},
		{"absolute path", "/tmp/test/file.txt", "/tmp/test/file.txt", false},
		{"relative path", "relative/path", "", true},
		{"file scheme no path", "file://", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := parseFileURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseFileURL(%q) expected error", tt.url)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFileURL(%q) unexpected error: %v", tt.url, err)
			}
			if d.path != tt.wantPath {
				t.Errorf("parseFileURL(%q) path = %q, want %q", tt.url, d.path, tt.wantPath)
			}
		})
	}
}

func TestDownload_SingleFile(t *testing.T) {
	// Create a source file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &Downloader{path: srcFile}
	tmpDir := t.TempDir()

	isFile, err := d.Download(context.Background(), tmpDir, settings.Defaults)
	if err != nil {
		t.Fatalf("Download() error: %v", err)
	}
	if !isFile {
		t.Error("Download() isFile = false, want true")
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "test.txt"))
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", string(data), "hello")
	}
}

func TestDownload_Directory(t *testing.T) {
	// Create a source directory with files.
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(srcDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "b.txt"), []byte("bbb"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &Downloader{path: srcDir}
	tmpDir := t.TempDir()

	isFile, err := d.Download(context.Background(), tmpDir, settings.Defaults)
	if err != nil {
		t.Fatalf("Download() error: %v", err)
	}
	if isFile {
		t.Error("Download() isFile = true, want false")
	}

	// Check files were copied.
	data, err := os.ReadFile(filepath.Join(tmpDir, "a.txt"))
	if err != nil {
		t.Fatalf("reading a.txt: %v", err)
	}
	if string(data) != "aaa" {
		t.Errorf("a.txt content = %q, want %q", string(data), "aaa")
	}

	data, err = os.ReadFile(filepath.Join(tmpDir, "sub", "b.txt"))
	if err != nil {
		t.Fatalf("reading sub/b.txt: %v", err)
	}
	if string(data) != "bbb" {
		t.Errorf("sub/b.txt content = %q, want %q", string(data), "bbb")
	}
}

func TestDownload_NonexistentPath(t *testing.T) {
	d := &Downloader{path: "/nonexistent/path/to/file"}
	tmpDir := t.TempDir()

	_, err := d.Download(context.Background(), tmpDir, settings.Defaults)
	if err == nil {
		t.Error("Download() expected error for nonexistent path")
	}
}

func TestPrefix(t *testing.T) {
	p := New()
	if p.Prefix() != "file" {
		t.Errorf("Prefix() = %q, want %q", p.Prefix(), "file")
	}
}

func TestPriority(t *testing.T) {
	p := New()
	if p.Priority() != 10 {
		t.Errorf("Priority() = %d, want %d", p.Priority(), 10)
	}
}
