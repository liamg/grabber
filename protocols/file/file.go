package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/liamg/grabber/protocols"
	"github.com/liamg/grabber/settings"
)

type Protocol struct{}

var _ protocols.Protocol = (*Protocol)(nil)

func New() *Protocol {
	return &Protocol{}
}

func (p *Protocol) Prefix() string {
	return "file"
}

func (p *Protocol) Priority() int {
	return 10
}

func (p *Protocol) Detect(rawURL string) (protocols.Downloadable, bool) {
	d, err := parseFileURL(rawURL)
	if err != nil {
		return nil, false
	}
	return d, true
}

func parseFileURL(rawURL string) (*Downloader, error) {
	// Handle file:// scheme.
	if strings.HasPrefix(rawURL, "file://") {
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, err
		}
		path := u.Path
		if path == "" {
			return nil, errors.New("no path specified")
		}
		return &Downloader{path: path}, nil
	}

	// Handle absolute paths.
	if filepath.IsAbs(rawURL) {
		return &Downloader{path: rawURL}, nil
	}

	if strings.HasPrefix(rawURL, "./") || strings.HasPrefix(rawURL, "../") {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get working directory: %w", err)
		}
		absPath := filepath.Join(wd, rawURL)
		return &Downloader{path: absPath}, nil
	}

	return nil, errors.New("not a file URL")
}

type Downloader struct {
	path string
}

var _ protocols.Downloadable = (*Downloader)(nil)

func (d *Downloader) Download(ctx context.Context, tmpDir string, s settings.Settings) (bool, error) {
	info, err := os.Stat(d.path)
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", d.path, err)
	}

	if info.IsDir() {
		return false, copyDir(d.path, tmpDir)
	}

	dst := filepath.Join(tmpDir, filepath.Base(d.path))
	return true, copyFile(d.path, dst)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}

		// filepath.Walk lstats, so a symlink arrives here as an ordinary entry
		// and has to be recreated rather than read. See copySymlink.
		if info.Mode()&os.ModeSymlink != 0 {
			return copySymlink(path, dstPath)
		}

		return copyFile(path, dstPath)
	})
}

// copySymlink recreates src's link at dst, pointing at the same target. The
// target is not resolved: following it would copy the target's bytes in place
// of the link, and a dangling link - or one pointing outside the source tree -
// would fail the whole copy or pull in a file that was never asked for.
func copySymlink(src, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// A link may already be there from an earlier copy into the same tree.
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(target, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
