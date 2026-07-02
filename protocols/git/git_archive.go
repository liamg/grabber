package git

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/liamg/grabber/settings"
)

// archiveURLOverride, when non-empty, replaces the URL fetchArchive downloads
// from. It exists so tests can point at a local httptest server rather than a
// real hosting platform.
var archiveURLOverride string

// fetchArchive downloads a tarball of d.ref from the hosting platform's HTTP
// API and extracts it into tmpDir. It is used as a fallback when git cannot
// retrieve a commit — e.g. orphaned commits that are unreachable from any ref.
// The hosting platform APIs (GitHub, GitLab, Bitbucket) can serve archives of
// arbitrary commits, including orphaned ones, which the git protocol cannot.
//
// Authentication reuses the same sources as clones (URL userinfo, configured
// HTTPS credentials, the git credential helper) and additionally falls back to
// well-known API token environment variables. SSH keys cannot be used here
// since this is an HTTP download, so SSH-only setups will fail for private
// repositories.
//
// The resulting directory is NOT a git repository — it contains only source
// files. If d.subdir is set, only files under it are extracted, with the subdir
// prefix stripped so its contents land directly in tmpDir (matching the git
// clone path).
func (d *Downloader) fetchArchive(ctx context.Context, tmpDir string, s settings.Settings) error {
	// Normalise SSH/SCP URLs to HTTPS so we can derive the host and path.
	u, err := url.Parse(sshToHTTPS(d.repoURL))
	if err != nil {
		return fmt.Errorf("parsing repo URL for archive fallback: %w", err)
	}

	aURL := archiveURLOverride
	if aURL == "" {
		aURL, err = archiveURL(u, d.ref)
		if err != nil {
			return err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, aURL, nil)
	if err != nil {
		return err
	}

	if username, password, ok := d.archiveCredentials(ctx, u, s); ok {
		req.SetBasicAuth(username, password)
	} else if token := tokenFromEnv(u.Hostname()); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading archive from %s: %w", aURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading archive from %s: HTTP %d", aURL, resp.StatusCode)
	}

	gzipR, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("decompressing archive: %w", err)
	}
	defer func() { _ = gzipR.Close() }()

	if err := extractArchive(gzipR, tmpDir, d.subdir); err != nil {
		return fmt.Errorf("extracting archive: %w", err)
	}

	return nil
}

// archiveCredentials resolves HTTP basic-auth credentials for an archive
// download, reusing the same sources as git clones: URL userinfo, configured
// HTTPS credentials, then the system git credential helper. The SSH placeholder
// user "git" is ignored since it is not a real credential.
func (d *Downloader) archiveCredentials(ctx context.Context, u *url.URL, s settings.Settings) (username, password string, ok bool) {
	if u.User != nil && u.User.Username() != "" && u.User.Username() != "git" {
		pw, _ := u.User.Password()
		return u.User.Username(), pw, true
	}

	if cred := s.MatchHTTPSCredential(u.String()); cred != nil {
		return cred.Username, cred.Password, true
	}

	if u.Scheme == "https" || u.Scheme == "http" {
		if auth := gitCredentialFill(ctx, u.Scheme, u.Hostname()); auth != nil {
			return auth.Username, auth.Password, true
		}
	}

	return "", "", false
}

// extractArchive reads a tar stream and writes its contents into dst. Hosting
// platform archives wrap everything in a single top-level directory (e.g.
// "owner-repo-<sha>/") which is stripped. If subdir is non-empty, only entries
// beneath it are written, with the subdir prefix removed relative to dst.
func extractArchive(r io.Reader, dst, subdir string) error {
	tarR := tar.NewReader(r)
	topDir := ""
	found := false

	for {
		hdr, err := tarR.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		if hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Typeflag == tar.TypeXHeader {
			continue
		}

		// Disallow parent traversal.
		if containsDotDot(hdr.Name) {
			return fmt.Errorf("archive entry contains '..': %s", hdr.Name)
		}

		// Discover and strip the single top-level directory.
		if topDir == "" {
			topDir = strings.SplitN(hdr.Name, "/", 2)[0] + "/"
		}
		rel := strings.TrimPrefix(hdr.Name, topDir)
		if rel == "" {
			// The top-level directory entry itself; skip it.
			continue
		}

		// If a subdir filter is set, skip entries outside it and strip its
		// prefix so contents land directly in dst.
		if subdir != "" {
			prefix := strings.TrimRight(subdir, "/") + "/"
			if !strings.HasPrefix(rel, prefix) {
				continue
			}
			rel = strings.TrimPrefix(rel, prefix)
			if rel == "" {
				continue
			}
		}

		found = true
		outPath := filepath.Join(dst, filepath.FromSlash(rel))

		if hdr.FileInfo().IsDir() {
			if err := os.MkdirAll(outPath, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := writeArchiveFile(outPath, tarR, hdr.FileInfo().Mode()); err != nil {
			return err
		}
	}

	if subdir != "" && !found {
		return fmt.Errorf("subdirectory %q not found in archive", subdir)
	}

	return nil
}

// writeArchiveFile writes the current tar entry to path, creating parent
// directories as needed.
func writeArchiveFile(path string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return nil
}

// containsDotDot reports whether v has a ".." path segment, which would allow
// escaping the destination directory during extraction.
func containsDotDot(v string) bool {
	if !strings.Contains(v, "..") {
		return false
	}
	for _, ent := range strings.FieldsFunc(v, func(r rune) bool { return r == '/' || r == '\\' }) {
		if ent == ".." {
			return true
		}
	}
	return false
}

// archiveURL constructs a tarball download URL for the given ref based on the
// hosting platform detected from u's hostname. The API endpoints are used
// rather than the web URLs because they resolve short commit SHAs.
func archiveURL(u *url.URL, ref string) (string, error) {
	owner, repo, err := parseOwnerRepo(u.Path)
	if err != nil {
		return "", err
	}

	host := strings.ToLower(u.Hostname())

	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com"):
		return fmt.Sprintf("https://api.github.com/repos/%s/%s/tarball/%s", owner, repo, ref), nil
	case host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com"):
		return fmt.Sprintf("https://gitlab.com/api/v4/projects/%s%%2F%s/repository/archive.tar.gz?sha=%s", owner, repo, ref), nil
	case host == "bitbucket.org" || strings.HasSuffix(host, ".bitbucket.org"):
		return fmt.Sprintf("https://bitbucket.org/%s/%s/get/%s.tar.gz", owner, repo, ref), nil
	default:
		return "", fmt.Errorf("unsupported git hosting platform %q for archive fallback", host)
	}
}

// tokenFromEnv returns an API token from well-known environment variables for
// the given host. It returns an empty string if no token is found.
func tokenFromEnv(host string) string {
	host = strings.ToLower(host)

	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com"):
		// GH_TOKEN is the GitHub CLI convention; GITHUB_TOKEN is the
		// widely-used CI/Actions variable.
		if t := os.Getenv("GH_TOKEN"); t != "" {
			return t
		}
		return os.Getenv("GITHUB_TOKEN")
	case host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com"):
		if t := os.Getenv("GITLAB_TOKEN"); t != "" {
			return t
		}
		return os.Getenv("GL_TOKEN")
	case host == "bitbucket.org" || strings.HasSuffix(host, ".bitbucket.org"):
		return os.Getenv("BITBUCKET_TOKEN")
	default:
		return ""
	}
}

// parseOwnerRepo extracts the owner and repository name from a URL path like
// "/owner/repo.git" or "/owner/repo".
func parseOwnerRepo(rawPath string) (owner, repo string, err error) {
	path := strings.TrimPrefix(rawPath, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("cannot parse owner/repo from path %q", rawPath)
	}
	return parts[0], parts[1], nil
}
