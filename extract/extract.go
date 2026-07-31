package extract

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"github.com/ulikunitz/xz"
)

// Extract extracts the archive at src into the dst directory with no size limit.
// It detects the archive type by file extension.
// Returns true if the file was extracted, false if the format is not recognised.
func Extract(src, dst string) (bool, error) {
	return ExtractWithLimit(src, dst, 0)
}

// ExtractWithLimit is Extract with a ceiling on the total number of bytes
// written across all entries, guarding against a decompression bomb. A maxBytes
// of 0 or less means unlimited.
func ExtractWithLimit(src, dst string, maxBytes int64) (bool, error) {
	ext := detectExtension(src)
	fn, ok := extractors[ext]
	if !ok {
		return false, nil
	}
	e := &extractor{remaining: maxBytes, limited: maxBytes > 0}
	return true, fn(e, src, dst)
}

// extractor carries the per-extraction size budget. remaining counts down as
// entries are written; when limited is false the budget is ignored.
type extractor struct {
	remaining int64
	limited   bool
}

type extractFunc func(e *extractor, src, dst string) error

var extractors = map[string]extractFunc{
	"tar":     (*extractor).extractTar,
	"tar.gz":  (*extractor).extractTarGzip,
	"tgz":     (*extractor).extractTarGzip,
	"tar.bz2": (*extractor).extractTarBzip2,
	"tbz2":    (*extractor).extractTarBzip2,
	"tar.xz":  (*extractor).extractTarXz,
	"txz":     (*extractor).extractTarXz,
	"tar.zst": (*extractor).extractTarZstd,
	"tzst":    (*extractor).extractTarZstd,
	"tar.lz4": (*extractor).extractTarLz4,
	"zip":     (*extractor).extractZip,
	"gz":      (*extractor).extractGzip,
	"bz2":     (*extractor).extractBzip2,
	"xz":      (*extractor).extractXz,
	"zst":     (*extractor).extractZstd,
	"lz4":     (*extractor).extractLz4,
}

// detectExtension returns the archive extension of the filename.
// It handles compound extensions like .tar.gz before checking single extensions.
func detectExtension(name string) string {
	lower := strings.ToLower(name)
	// Check compound extensions first.
	for _, ext := range []string{"tar.gz", "tar.bz2", "tar.xz", "tar.zst", "tar.lz4"} {
		if strings.HasSuffix(lower, "."+ext) {
			return ext
		}
	}
	ext := strings.TrimPrefix(filepath.Ext(lower), ".")
	return ext
}

// --- tar helpers ---

func (e *extractor) extractTar(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	return e.untar(f, dst)
}

func (e *extractor) extractTarGzip(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()

	return e.untar(gr, dst)
}

func (e *extractor) extractTarBzip2(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	return e.untar(bzip2.NewReader(f), dst)
}

func (e *extractor) extractTarXz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	xr, err := xz.NewReader(f)
	if err != nil {
		return err
	}

	return e.untar(xr, dst)
}

func (e *extractor) extractTarZstd(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	zr, err := zstd.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()

	return e.untar(zr, dst)
}

func (e *extractor) extractTarLz4(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	return e.untar(lz4.NewReader(f), dst)
}

func (e *extractor) untar(r io.Reader, dst string) error {
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dst, header.Name)

		// Prevent path traversal.
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("tar entry %q attempts path traversal", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, safeMode(header.FileInfo().Mode())); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := e.writeFile(target, tr, safeMode(header.FileInfo().Mode())); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- zip ---

func (e *extractor) extractZip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		target := filepath.Join(dst, f.Name)

		// Prevent path traversal.
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry %q attempts path traversal", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, safeMode(f.Mode())); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}

		err = e.writeFile(target, rc, safeMode(f.Mode()))
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// --- single-file decompressors ---

func (e *extractor) extractGzip(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()

	outName := strings.TrimSuffix(filepath.Base(src), ".gz")
	return e.writeFile(filepath.Join(dst, outName), gr, 0o644)
}

func (e *extractor) extractBzip2(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	outName := strings.TrimSuffix(filepath.Base(src), ".bz2")
	return e.writeFile(filepath.Join(dst, outName), bzip2.NewReader(f), 0o644)
}

func (e *extractor) extractXz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	xr, err := xz.NewReader(f)
	if err != nil {
		return err
	}

	outName := strings.TrimSuffix(filepath.Base(src), ".xz")
	return e.writeFile(filepath.Join(dst, outName), xr, 0o644)
}

func (e *extractor) extractZstd(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	zr, err := zstd.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()

	outName := strings.TrimSuffix(filepath.Base(src), ".zst")
	return e.writeFile(filepath.Join(dst, outName), zr, 0o644)
}

func (e *extractor) extractLz4(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	outName := strings.TrimSuffix(filepath.Base(src), ".lz4")
	return e.writeFile(filepath.Join(dst, outName), lz4.NewReader(f), 0o644)
}

// --- helpers ---

// writeFile writes r to path, counting the bytes against the extraction budget
// so the archive as a whole cannot exceed it.
func (e *extractor) writeFile(path string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()

	if !e.limited {
		_, err = io.Copy(f, r)
		return err
	}

	// Read one past the remaining budget so an entry that would exhaust it is
	// detected even when remaining has reached exactly zero.
	n, err := io.Copy(f, io.LimitReader(r, e.remaining+1))
	if err != nil {
		return err
	}
	if n > e.remaining {
		return fmt.Errorf("archive exceeds maximum extraction size")
	}
	e.remaining -= n
	return nil
}

// safeMode strips the setuid, setgid and sticky bits from a mode carried in an
// archive, so an untrusted archive cannot land a setuid/setgid file on disk. It
// keeps only the permission bits (rwx for user/group/other).
func safeMode(mode os.FileMode) os.FileMode {
	return mode.Perm()
}
