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

// Extract extracts the archive at src into the dst directory.
// It detects the archive type by file extension.
// Returns true if the file was extracted, false if the format is not recognised.
func Extract(src, dst string) (bool, error) {
	ext := detectExtension(src)
	fn, ok := extractors[ext]
	if !ok {
		return false, nil
	}
	return true, fn(src, dst)
}

type extractFunc func(src, dst string) error

var extractors = map[string]extractFunc{
	"tar":     extractTar,
	"tar.gz":  extractTarGzip,
	"tgz":     extractTarGzip,
	"tar.bz2": extractTarBzip2,
	"tbz2":    extractTarBzip2,
	"tar.xz":  extractTarXz,
	"txz":     extractTarXz,
	"tar.zst": extractTarZstd,
	"tzst":    extractTarZstd,
	"tar.lz4": extractTarLz4,
	"zip":     extractZip,
	"gz":      extractGzip,
	"bz2":     extractBzip2,
	"xz":      extractXz,
	"zst":     extractZstd,
	"lz4":     extractLz4,
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

func extractTar(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	return untar(f, dst)
}

func extractTarGzip(src, dst string) error {
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

	return untar(gr, dst)
}

func extractTarBzip2(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	return untar(bzip2.NewReader(f), dst)
}

func extractTarXz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	xr, err := xz.NewReader(f)
	if err != nil {
		return err
	}

	return untar(xr, dst)
}

func extractTarZstd(src, dst string) error {
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

	return untar(zr, dst)
}

func extractTarLz4(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	return untar(lz4.NewReader(f), dst)
}

func untar(r io.Reader, dst string) error {
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
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeFile(target, tr, os.FileMode(header.Mode)); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- zip ---

func extractZip(src, dst string) error {
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
			if err := os.MkdirAll(target, f.Mode()); err != nil {
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

		err = writeFile(target, rc, f.Mode())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// --- single-file decompressors ---

func extractGzip(src, dst string) error {
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
	return writeFile(filepath.Join(dst, outName), gr, 0o644)
}

func extractBzip2(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	outName := strings.TrimSuffix(filepath.Base(src), ".bz2")
	return writeFile(filepath.Join(dst, outName), bzip2.NewReader(f), 0o644)
}

func extractXz(src, dst string) error {
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
	return writeFile(filepath.Join(dst, outName), xr, 0o644)
}

func extractZstd(src, dst string) error {
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
	return writeFile(filepath.Join(dst, outName), zr, 0o644)
}

func extractLz4(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	outName := strings.TrimSuffix(filepath.Base(src), ".lz4")
	return writeFile(filepath.Join(dst, outName), lz4.NewReader(f), 0o644)
}

// --- helpers ---

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, r)
	return err
}
