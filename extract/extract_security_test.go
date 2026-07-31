package extract

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// tarWith builds a tar archive from the given entries (name -> spec).
func tarWith(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     e.mode,
			Size:     int64(len(e.body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type tarEntry struct {
	name string
	mode int64
	body string
}

func TestExtractWithLimit(t *testing.T) {
	t.Run("a single entry over the limit is rejected", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "big.tar")
		os.WriteFile(src, tarWith(t, []tarEntry{{"big.txt", 0o644, "0123456789"}}), 0o644)

		if _, err := ExtractWithLimit(src, t.TempDir(), 4); err == nil {
			t.Fatal("expected an over-limit entry to be rejected")
		}
	})

	t.Run("entries under the limit individually but over it together are rejected", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "two.tar")
		os.WriteFile(src, tarWith(t, []tarEntry{
			{"a.txt", 0o644, "aaaa"},
			{"b.txt", 0o644, "bbbb"},
		}), 0o644)

		// Budget of 6 bytes: 4 + 4 = 8 total must fail even though neither file
		// alone exceeds it.
		if _, err := ExtractWithLimit(src, t.TempDir(), 6); err == nil {
			t.Fatal("expected the cumulative budget to be enforced across entries")
		}
	})

	t.Run("within the limit succeeds", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "ok.tar")
		os.WriteFile(src, tarWith(t, []tarEntry{{"a.txt", 0o644, "hello"}}), 0o644)

		if _, err := ExtractWithLimit(src, t.TempDir(), 1024); err != nil {
			t.Fatalf("expected extraction within the limit to succeed, got %v", err)
		}
	})
}

func TestExtractStripsSetuid(t *testing.T) {
	src := filepath.Join(t.TempDir(), "suid.tar")
	// 0o4755 = setuid + rwxr-xr-x.
	os.WriteFile(src, tarWith(t, []tarEntry{{"tool", 0o4755, "#!/bin/sh\n"}}), 0o644)

	dst := t.TempDir()
	if _, err := Extract(src, dst); err != nil {
		t.Fatalf("extract: %v", err)
	}

	info, err := os.Stat(filepath.Join(dst, "tool"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSetuid != 0 {
		t.Error("extracted file kept its setuid bit")
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("permission bits = %o, want 0755", info.Mode().Perm())
	}
}
