package hg

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/liamg/grabber/protocols"
	"github.com/liamg/grabber/settings"
)

type Protocol struct{}

var (
	_ protocols.Protocol       = (*Protocol)(nil)
	_ protocols.ForcedDetector = (*Protocol)(nil)
)

func New() *Protocol {
	return &Protocol{}
}

func (p *Protocol) Prefix() string {
	return "hg"
}

func (p *Protocol) Priority() int {
	return 80
}

// knownHgHosts are hostnames known to host Mercurial repositories.
var knownHgHosts = []string{
	"bitbucket.org",
}

func (p *Protocol) Detect(rawURL string) (protocols.Downloadable, bool) {
	d, err := parseHgURL(rawURL, false)
	if err != nil {
		return nil, false
	}
	return d, true
}

// DetectForced accepts any URL it can parse, without requiring a host we happen
// to know hosts Mercurial. An "hg::" prefix is the caller stating the remote is
// a Mercurial repository, and a self-hosted one is on no such list.
func (p *Protocol) DetectForced(rawURL string) (protocols.Downloadable, bool) {
	d, err := parseHgURL(rawURL, true)
	if err != nil {
		return nil, false
	}
	return d, true
}

// parseHgURL parses a Mercurial URL and extracts the repo URL, revision, and subdirectory.
//
// Supported formats:
//   - https://bitbucket.org/user/repo
//   - https://bitbucket.org/user/repo//subdir
//   - https://bitbucket.org/user/repo?rev=v1.0.0
//
// forced reports whether the caller used the "hg::" prefix, which commits them
// to this protocol and so skips the known-host check.
func parseHgURL(rawURL string, forced bool) (*Downloader, error) {
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	if !forced && !isHgURL(u) {
		return nil, errors.New("not a Mercurial URL")
	}

	repoPath, subdir := splitSubdir(u.Path)

	rev := u.Query().Get("rev")

	u.Path = repoPath
	u.RawQuery = ""
	u.Fragment = ""

	return &Downloader{
		repoURL: u.String(),
		rev:     rev,
		subdir:  subdir,
	}, nil
}

func isHgURL(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	for _, known := range knownHgHosts {
		if host == known {
			return true
		}
	}
	return false
}

func splitSubdir(path string) (string, string) {
	if idx := strings.Index(path, "//"); idx != -1 {
		return path[:idx], strings.TrimPrefix(path[idx+2:], "/")
	}
	return path, ""
}

type Downloader struct {
	repoURL string
	rev     string
	subdir  string
}

var _ protocols.Downloadable = (*Downloader)(nil)

func (d *Downloader) Download(ctx context.Context, tmpDir string, s settings.Settings) (bool, error) {
	if s.NoSystemFallback {
		return false, errors.New("mercurial support is disabled when system fallback is off (it requires the hg subprocess)")
	}

	// hg dials in a subprocess (not through our transport), so apply the SSRF
	// guard as a pre-fetch check on the repo host. Also reject a URL whose host
	// begins with "-": passed to the hg subprocess it could be read as an option
	// (the ssh transport's -oProxyCommand argument-injection class), and we rely
	// on hg's own protections for nothing.
	if u, err := url.Parse(d.repoURL); err == nil {
		if strings.HasPrefix(u.Hostname(), "-") {
			return false, fmt.Errorf("refusing to clone from a URL whose host begins with '-': %q", d.repoURL)
		}
		if err := s.CheckSSRFHost(ctx, u.Hostname()); err != nil {
			return false, err
		}
	}

	if _, err := exec.LookPath("hg"); err != nil {
		return false, errors.New("hg (Mercurial) executable not found in PATH")
	}

	cloneDir := tmpDir
	if d.subdir != "" {
		cloneDir = filepath.Join(tmpDir, "_clone")
	}

	args := []string{"clone"}
	if d.rev != "" {
		args = append(args, "--rev", d.rev)
	}
	// "--" ends option parsing, so neither the URL nor the destination can be
	// read as a flag even if it begins with "-".
	args = append(args, "--", d.repoURL, cloneDir)

	cmd := exec.CommandContext(ctx, "hg", args...)
	cmd.Env = os.Environ()
	if output, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("cloning mercurial repo: %s: %w", string(output), err)
	}

	// Remove .hg directory — we just want the content.
	os.RemoveAll(filepath.Join(cloneDir, ".hg"))

	if d.subdir != "" {
		srcDir := filepath.Join(cloneDir, d.subdir)
		info, err := os.Stat(srcDir)
		if err != nil {
			return false, fmt.Errorf("subdirectory %q not found in repo: %w", d.subdir, err)
		}
		if !info.IsDir() {
			return false, fmt.Errorf("subdirectory %q is not a directory", d.subdir)
		}

		entries, err := os.ReadDir(srcDir)
		if err != nil {
			return false, err
		}
		for _, e := range entries {
			src := filepath.Join(srcDir, e.Name())
			dst := filepath.Join(tmpDir, e.Name())
			if err := os.Rename(src, dst); err != nil {
				return false, fmt.Errorf("moving %s: %w", e.Name(), err)
			}
		}

		os.RemoveAll(cloneDir)
	}

	return false, nil
}
