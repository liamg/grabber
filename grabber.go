package grabber

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/liamg/grabber/extract"
	"github.com/liamg/grabber/protocols"
	fileprotocol "github.com/liamg/grabber/protocols/file"
	"github.com/liamg/grabber/protocols/gcs"
	gitprotocol "github.com/liamg/grabber/protocols/git"
	"github.com/liamg/grabber/protocols/hg"
	httpprotocol "github.com/liamg/grabber/protocols/http"
	"github.com/liamg/grabber/protocols/oci"
	"github.com/liamg/grabber/protocols/s3"
	"github.com/liamg/grabber/settings"
)

type Grabber struct {
	settings  settings.Settings
	protocols []protocols.Protocol
}

type Option func(*Grabber)

var allProtocols = []protocols.Protocol{
	gitprotocol.New(),
	hg.New(),
	s3.New(),
	gcs.New(),
	oci.New(),
	httpprotocol.New(),
	fileprotocol.New(),
}

func init() {
	sort.Slice(allProtocols, func(i, j int) bool {
		if allProtocols[i].Priority() == allProtocols[j].Priority() {
			return allProtocols[i].Prefix() < allProtocols[j].Prefix()
		}
		return allProtocols[i].Priority() > allProtocols[j].Priority()
	})
}

func New(options ...Option) *Grabber {
	g := &Grabber{
		settings:  settings.Defaults,
		protocols: allProtocols,
	}
	for _, option := range options {
		option(g)
	}
	return g
}

var ErrUnsupportedURL = errors.New("unsupported URL")

func (g *Grabber) Grab(ctx context.Context, rawURL, dst string) error {
	return g.grab(ctx, rawURL, dst, checksumSpec{})
}

// GrabWithSHA256Checksum downloads the content at the given URL to dst, and
// verifies the downloaded file has the expected SHA-256 hash. The expected value
// should be hex-encoded. If the URL also contains a ?checksum= query parameter,
// the explicit argument takes precedence.
func (g *Grabber) GrabWithSHA256Checksum(ctx context.Context, rawURL, dst, expected string) error {
	return g.grab(ctx, rawURL, dst, checksumSpec{algorithm: "sha256", expected: expected})
}

func (g *Grabber) grab(ctx context.Context, rawURL, dst string, explicit checksumSpec) error {

	tmpDir := filepath.Join(g.settings.TemporaryDirectory, "grabber", uuid.NewString())
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Extract checksum from URL query param if present.
	cleanURL, urlChecksum := extractChecksum(rawURL)

	// Explicit checksum takes precedence over URL param.
	checksum := explicit
	if checksum.algorithm == "" {
		checksum = urlChecksum
	}

	isFile, err := g.download(ctx, cleanURL, tmpDir)
	if err != nil {
		return fmt.Errorf("failed to download content: %w", err)
	}

	if checksum.algorithm != "" {
		if !isFile {
			return errors.New("checksum verification is only supported for single-file downloads")
		}
		if err := verifyChecksum(tmpDir, checksum); err != nil {
			return fmt.Errorf("checksum verification failed: %w", err)
		}
	}

	if isFile && g.settings.EnableAutoExtract {
		entries, err := os.ReadDir(tmpDir)
		if err != nil {
			return fmt.Errorf("reading temp directory: %w", err)
		}
		if len(entries) != 1 {
			return fmt.Errorf("expected exactly one file in temp directory, found %d", len(entries))
		}
		archive := filepath.Join(tmpDir, entries[0].Name())
		extractDir := filepath.Join(tmpDir, "_extracted")
		if extracted, err := extract.Extract(archive, extractDir); err != nil {
			return fmt.Errorf("extracting archive: %w", err)
		} else if extracted {
			// Replace tmpDir contents with extracted contents.
			if err := os.Remove(archive); err != nil {
				return fmt.Errorf("removing archive after extraction: %w", err)
			}
			tmpDir = extractDir
		}
	}

	if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
		// dst doesn't exist — try a rename (fast path)
		if err := os.Rename(tmpDir, dst); err == nil {
			return nil
		}
		// rename can fail across filesystems, fall through to copy
	}

	return copyDir(tmpDir, dst)
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

		return copyFile(path, dstPath, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

type checksumSpec struct {
	algorithm string // e.g. "md5", "sha1", "sha256", "sha512"
	expected  string // hex-encoded expected hash
}

// extractChecksum strips the ?checksum=algo:hex query parameter from the URL,
// returning the cleaned URL and the parsed checksum. If no checksum param is
// present, the returned checksumSpec has empty fields.
func extractChecksum(rawURL string) (string, checksumSpec) {
	// Handle force-prefix (e.g. "s3::https://...?checksum=sha256:abc").
	// We need to parse the part after :: if present.
	prefix := ""
	toParse := rawURL
	if p, remainder, ok := strings.Cut(rawURL, "::"); ok {
		prefix = p + "::"
		toParse = remainder
	}

	// Only parse if it looks like it has a query string with checksum.
	if !strings.Contains(toParse, "checksum=") {
		return rawURL, checksumSpec{}
	}

	u, err := url.Parse(toParse)
	if err != nil {
		return rawURL, checksumSpec{}
	}

	q := u.Query()
	checksumVal := q.Get("checksum")
	if checksumVal == "" {
		return rawURL, checksumSpec{}
	}

	algo, expected, ok := strings.Cut(checksumVal, ":")
	if !ok {
		// No algorithm prefix — default to sha256.
		algo = "sha256"
		expected = checksumVal
	}
	if algo == "" || expected == "" {
		return rawURL, checksumSpec{}
	}

	q.Del("checksum")
	u.RawQuery = q.Encode()
	return prefix + u.String(), checksumSpec{algorithm: algo, expected: expected}
}

func newHash(algorithm string) (hash.Hash, error) {
	switch strings.ToLower(algorithm) {
	case "md5":
		return md5.New(), nil
	case "sha1":
		return sha1.New(), nil
	case "sha256":
		return sha256.New(), nil
	case "sha512":
		return sha512.New(), nil
	default:
		return nil, fmt.Errorf("unsupported checksum algorithm: %q", algorithm)
	}
}

// verifyChecksum hashes the single file in tmpDir and compares it to the expected checksum.
func verifyChecksum(tmpDir string, checksum checksumSpec) error {
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return fmt.Errorf("reading temp directory: %w", err)
	}
	if len(entries) != 1 {
		return fmt.Errorf("expected exactly one file in temp directory, found %d", len(entries))
	}

	filePath := filepath.Join(tmpDir, entries[0].Name())

	h, err := newHash(checksum.algorithm)
	if err != nil {
		return err
	}

	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("opening file for checksum: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("reading file for checksum: %w", err)
	}

	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actual, checksum.expected) {
		return fmt.Errorf("checksum mismatch: expected %s:%s, got %s:%s",
			checksum.algorithm, checksum.expected,
			checksum.algorithm, actual)
	}

	return nil
}

func (g *Grabber) download(ctx context.Context, url, tmpDir string) (bool, error) {

	if prefix, remainder, ok := strings.Cut(url, "::"); ok {
		for _, protocol := range g.protocols {
			if protocol.Prefix() == prefix {
				// The force-prefix commits to this protocol, so let it detect
				// more permissively than the shared auto-detection path if it
				// implements ForcedDetector (e.g. s3:: accepting custom
				// S3-compatible endpoints that a bare URL must not match).
				detect := protocol.Detect
				if fd, ok := protocol.(protocols.ForcedDetector); ok {
					detect = fd.DetectForced
				}
				if downloadable, ok := detect(remainder); ok {
					return downloadable.Download(ctx, tmpDir, g.settings)
				} else {
					return false, fmt.Errorf("protocol %q detected but could not handle URL: %w", prefix, ErrUnsupportedURL)
				}
			}
		}
	}

	for _, protocol := range g.protocols {
		if downloadable, ok := protocol.Detect(url); ok {
			return downloadable.Download(ctx, tmpDir, g.settings)
		}
	}

	return false, ErrUnsupportedURL
}
