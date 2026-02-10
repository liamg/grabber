package extract

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"github.com/ulikunitz/xz"
)

func TestDetectExtension(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"file.tar", "tar"},
		{"file.tar.gz", "tar.gz"},
		{"file.tgz", "tgz"},
		{"file.tar.bz2", "tar.bz2"},
		{"file.tbz2", "tbz2"},
		{"file.tar.xz", "tar.xz"},
		{"file.txz", "txz"},
		{"file.tar.zst", "tar.zst"},
		{"file.tzst", "tzst"},
		{"file.tar.lz4", "tar.lz4"},
		{"file.zip", "zip"},
		{"file.gz", "gz"},
		{"file.bz2", "bz2"},
		{"file.xz", "xz"},
		{"file.zst", "zst"},
		{"file.lz4", "lz4"},
		{"FILE.TAR.GZ", "tar.gz"},
		{"file.txt", "txt"},
		{"file", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectExtension(tt.name)
			if got != tt.want {
				t.Errorf("detectExtension(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestExtract_UnknownFormat(t *testing.T) {
	src := filepath.Join(t.TempDir(), "file.txt")
	os.WriteFile(src, []byte("hello"), 0o644)

	extracted, err := Extract(src, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extracted {
		t.Error("expected extracted=false for unknown format")
	}
}

// --- tar ---

func TestExtract_Tar(t *testing.T) {
	src := filepath.Join(t.TempDir(), "test.tar")
	writeTar(t, src, map[string]string{
		"hello.txt":      "hello world",
		"sub/nested.txt": "nested content",
	})

	dst := t.TempDir()
	extracted, err := Extract(src, dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extracted {
		t.Fatal("expected extracted=true")
	}

	assertFile(t, filepath.Join(dst, "hello.txt"), "hello world")
	assertFile(t, filepath.Join(dst, "sub/nested.txt"), "nested content")
}

// --- tar.gz / tgz ---

func TestExtract_TarGz(t *testing.T) {
	tarData := tarBytes(t, map[string]string{"a.txt": "aaa"})
	src := filepath.Join(t.TempDir(), "test.tar.gz")
	writeGzip(t, src, tarData)

	dst := t.TempDir()
	extracted, err := Extract(src, dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extracted {
		t.Fatal("expected extracted=true")
	}
	assertFile(t, filepath.Join(dst, "a.txt"), "aaa")
}

func TestExtract_Tgz(t *testing.T) {
	tarData := tarBytes(t, map[string]string{"b.txt": "bbb"})
	src := filepath.Join(t.TempDir(), "test.tgz")
	writeGzip(t, src, tarData)

	dst := t.TempDir()
	extracted, err := Extract(src, dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extracted {
		t.Fatal("expected extracted=true")
	}
	assertFile(t, filepath.Join(dst, "b.txt"), "bbb")
}

// --- tar.bz2 / tbz2 ---

func TestExtract_TarBz2(t *testing.T) {
	if _, err := exec.LookPath("bzip2"); err != nil {
		t.Skip("bzip2 not available, skipping")
	}

	tarData := tarBytes(t, map[string]string{"c.txt": "ccc"})
	src := filepath.Join(t.TempDir(), "test.tar.bz2")
	writeBzip2(t, src, tarData)

	dst := t.TempDir()
	extracted, err := Extract(src, dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extracted {
		t.Fatal("expected extracted=true")
	}
	assertFile(t, filepath.Join(dst, "c.txt"), "ccc")
}

// --- tar.xz / txz ---

func TestExtract_TarXz(t *testing.T) {
	tarData := tarBytes(t, map[string]string{"d.txt": "ddd"})
	src := filepath.Join(t.TempDir(), "test.tar.xz")
	writeXz(t, src, tarData)

	dst := t.TempDir()
	extracted, err := Extract(src, dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extracted {
		t.Fatal("expected extracted=true")
	}
	assertFile(t, filepath.Join(dst, "d.txt"), "ddd")
}

// --- tar.zst / tzst ---

func TestExtract_TarZstd(t *testing.T) {
	tarData := tarBytes(t, map[string]string{"e.txt": "eee"})
	src := filepath.Join(t.TempDir(), "test.tar.zst")
	writeZstd(t, src, tarData)

	dst := t.TempDir()
	extracted, err := Extract(src, dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extracted {
		t.Fatal("expected extracted=true")
	}
	assertFile(t, filepath.Join(dst, "e.txt"), "eee")
}

// --- tar.lz4 ---

func TestExtract_TarLz4(t *testing.T) {
	tarData := tarBytes(t, map[string]string{"f.txt": "fff"})
	src := filepath.Join(t.TempDir(), "test.tar.lz4")
	writeLz4(t, src, tarData)

	dst := t.TempDir()
	extracted, err := Extract(src, dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extracted {
		t.Fatal("expected extracted=true")
	}
	assertFile(t, filepath.Join(dst, "f.txt"), "fff")
}

// --- zip ---

func TestExtract_Zip(t *testing.T) {
	src := filepath.Join(t.TempDir(), "test.zip")
	writeZip(t, src, map[string]string{
		"hello.txt":      "hello zip",
		"sub/nested.txt": "nested zip",
	})

	dst := t.TempDir()
	extracted, err := Extract(src, dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extracted {
		t.Fatal("expected extracted=true")
	}

	assertFile(t, filepath.Join(dst, "hello.txt"), "hello zip")
	assertFile(t, filepath.Join(dst, "sub/nested.txt"), "nested zip")
}

// --- single-file decompressors ---

func TestExtract_Gzip(t *testing.T) {
	src := filepath.Join(t.TempDir(), "data.txt.gz")
	writeGzip(t, src, []byte("gzip content"))

	dst := t.TempDir()
	extracted, err := Extract(src, dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extracted {
		t.Fatal("expected extracted=true")
	}
	assertFile(t, filepath.Join(dst, "data.txt"), "gzip content")
}

func TestExtract_Bzip2(t *testing.T) {
	if _, err := exec.LookPath("bzip2"); err != nil {
		t.Skip("bzip2 not available, skipping")
	}

	src := filepath.Join(t.TempDir(), "data.txt.bz2")
	writeBzip2(t, src, []byte("bzip2 content"))

	dst := t.TempDir()
	extracted, err := Extract(src, dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extracted {
		t.Fatal("expected extracted=true")
	}
	assertFile(t, filepath.Join(dst, "data.txt"), "bzip2 content")
}

func TestExtract_Xz(t *testing.T) {
	src := filepath.Join(t.TempDir(), "data.txt.xz")
	writeXz(t, src, []byte("xz content"))

	dst := t.TempDir()
	extracted, err := Extract(src, dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extracted {
		t.Fatal("expected extracted=true")
	}
	assertFile(t, filepath.Join(dst, "data.txt"), "xz content")
}

func TestExtract_Zstd(t *testing.T) {
	src := filepath.Join(t.TempDir(), "data.txt.zst")
	writeZstd(t, src, []byte("zstd content"))

	dst := t.TempDir()
	extracted, err := Extract(src, dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extracted {
		t.Fatal("expected extracted=true")
	}
	assertFile(t, filepath.Join(dst, "data.txt"), "zstd content")
}

func TestExtract_Lz4(t *testing.T) {
	src := filepath.Join(t.TempDir(), "data.txt.lz4")
	writeLz4(t, src, []byte("lz4 content"))

	dst := t.TempDir()
	extracted, err := Extract(src, dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extracted {
		t.Fatal("expected extracted=true")
	}
	assertFile(t, filepath.Join(dst, "data.txt"), "lz4 content")
}

// --- path traversal ---

func TestExtract_Tar_PathTraversal(t *testing.T) {
	src := filepath.Join(t.TempDir(), "evil.tar")
	writeTarRaw(t, src, map[string]string{
		"../../../etc/passwd": "pwned",
	})

	dst := t.TempDir()
	_, err := Extract(src, dst)
	if err == nil {
		t.Fatal("expected error for path traversal in tar")
	}
}

func TestExtract_Zip_PathTraversal(t *testing.T) {
	src := filepath.Join(t.TempDir(), "evil.zip")
	writeZip(t, src, map[string]string{
		"../../../etc/passwd": "pwned",
	})

	dst := t.TempDir()
	_, err := Extract(src, dst)
	if err == nil {
		t.Fatal("expected error for path traversal in zip")
	}
}

// --- helpers ---

func assertFile(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(data) != expected {
		t.Errorf("%s: got %q, want %q", path, string(data), expected)
	}
}

// tarBytes creates a tar archive in memory and returns the raw bytes.
func tarBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	return buf.Bytes()
}

// writeTar writes a tar file to disk.
func writeTar(t *testing.T, dst string, files map[string]string) {
	t.Helper()
	os.WriteFile(dst, tarBytes(t, files), 0o644)
}

// writeTarRaw writes a tar with exact paths (for path traversal testing).
func writeTarRaw(t *testing.T, dst string, files map[string]string) {
	t.Helper()
	os.WriteFile(dst, tarBytes(t, files), 0o644)
}

func writeGzip(t *testing.T, dst string, data []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(data)
	w.Close()
	os.WriteFile(dst, buf.Bytes(), 0o644)
}

// writeBzip2 creates a bzip2-compressed file using the bzip2 command.
func writeBzip2(t *testing.T, dst string, data []byte) {
	t.Helper()
	cmd := exec.Command("bzip2", "--stdout")
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bzip2 compress: %v", err)
	}
	os.WriteFile(dst, out, 0o644)
}

func writeXz(t *testing.T, dst string, data []byte) {
	t.Helper()
	var buf bytes.Buffer
	w, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
	w.Close()
	os.WriteFile(dst, buf.Bytes(), 0o644)
}

func writeZstd(t *testing.T, dst string, data []byte) {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
	w.Close()
	os.WriteFile(dst, buf.Bytes(), 0o644)
}

func writeLz4(t *testing.T, dst string, data []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := lz4.NewWriter(&buf)
	io.Copy(w, bytes.NewReader(data))
	w.Close()
	os.WriteFile(dst, buf.Bytes(), 0o644)
}

func writeZip(t *testing.T, dst string, files map[string]string) {
	t.Helper()
	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	zw.Close()
}
