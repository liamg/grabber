package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	cryptossh "golang.org/x/crypto/ssh"

	"github.com/liamg/grabber/protocols"
	"github.com/liamg/grabber/settings"
)

type Protocol struct{}

var _ protocols.Protocol = (*Protocol)(nil)

func New() *Protocol {
	return &Protocol{}
}

func (p *Protocol) Prefix() string {
	return "git"
}

func (p *Protocol) Priority() int {
	return 90
}

// scpPattern matches SCP-style Git URLs like git@github.com:user/repo.git
var scpPattern = regexp.MustCompile(`^(?:[a-zA-Z0-9_]+)@[a-zA-Z0-9._-]+:`)

func (p *Protocol) Detect(rawURL string) (protocols.Downloadable, bool) {
	d, err := parseGitURL(rawURL)
	if err != nil {
		return nil, false
	}
	return d, true
}

// parseGitURL parses a Git URL and extracts the repo URL, ref, subdir, and depth.
//
// Supported formats:
//   - https://github.com/user/repo.git
//   - https://github.com/user/repo.git//subdir
//   - https://github.com/user/repo.git?ref=v1.0.0&depth=1
//   - ssh://git@github.com/user/repo.git
//   - git@github.com:user/repo.git
//   - github.com/user/repo (detected by known hosts)
//
// NOTE: all of the above formats can also include the prefix "git::" to help with detection, but the prefix is stripped before parsing.
func parseGitURL(rawURL string) (*Downloader, error) {
	// Check for SCP-style URLs first (git@host:user/repo.git).
	if scpPattern.MatchString(rawURL) {
		return parseSCPURL(rawURL)
	}

	// Add scheme if missing.
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	if !isGitURL(u) {
		return nil, errors.New("not a Git URL")
	}

	// Extract subdir from // syntax.
	repoPath, subdir := splitSubdir(u.Path)

	// Extract query params.
	ref := u.Query().Get("ref")
	depth := parseDepth(u.Query().Get("depth"))

	// Rebuild clean repo URL without query params and subdir.
	u.Path = repoPath
	u.RawQuery = ""
	u.Fragment = ""

	return &Downloader{
		repoURL: u.String(),
		ref:     ref,
		subdir:  subdir,
		depth:   depth,
	}, nil
}

func parseSCPURL(rawURL string) (*Downloader, error) {
	// Split off query string if present: git@github.com:user/repo.git?ref=main
	queryStr := ""
	if idx := strings.Index(rawURL, "?"); idx != -1 {
		queryStr = rawURL[idx+1:]
		rawURL = rawURL[:idx]
	}

	// Split off subdir: git@github.com:user/repo.git//subdir
	repoURL, subdir := splitSubdir(rawURL)

	ref := ""
	depth := 0
	if queryStr != "" {
		q, err := url.ParseQuery(queryStr)
		if err != nil {
			return nil, err
		}
		ref = q.Get("ref")
		depth = parseDepth(q.Get("depth"))
	}

	return &Downloader{
		repoURL: repoURL,
		ref:     ref,
		subdir:  subdir,
		depth:   depth,
	}, nil
}

// knownGitHosts are hostnames that are known to be Git hosting providers.
var knownGitHosts = []string{
	"github.com",
	"gitlab.com",
	"bitbucket.org",
	"codeberg.org",
	"dev.azure.com",
	"sr.ht",
}

func isGitURL(u *url.URL) bool {
	// SSH scheme is always Git.
	if u.Scheme == "ssh" {
		return true
	}

	// .git suffix is a strong signal.
	if strings.HasSuffix(u.Path, ".git") || strings.Contains(u.Path, ".git//") {
		return true
	}

	// Known Git hosts.
	host := strings.ToLower(u.Hostname())
	for _, known := range knownGitHosts {
		if host == known {
			return true
		}
	}

	return false
}

// splitSubdir splits a path on "//" into the repo path and subdirectory.
func splitSubdir(path string) (string, string) {
	if idx := strings.Index(path, "//"); idx != -1 {
		return path[:idx], strings.TrimPrefix(path[idx+2:], "/")
	}
	return path, ""
}

func parseDepth(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

type Downloader struct {
	repoURL string
	ref     string
	subdir  string
	depth   int
}

var _ protocols.Downloadable = (*Downloader)(nil)

func (d *Downloader) Download(ctx context.Context, tmpDir string, s settings.Settings) (bool, error) {
	if s.Git.SparseCheckout && d.subdir == "" {
		return false, errors.New("sparse checkout requires a subdirectory (use // syntax)")
	}

	// Convert SSH URLs to HTTPS if configured.
	if s.Git.SSHToHTTPS {
		d.repoURL = sshToHTTPS(d.repoURL)
	}

	err := d.gitDownload(ctx, tmpDir, s)
	if err == nil {
		return false, nil
	}

	// If git could not retrieve a commit hash, fall back to the hosting
	// platform's HTTP archive endpoint. This handles orphaned commits that are
	// unreachable via the git protocol but still downloadable via the API.
	if looksLikeCommitHash(d.ref) {
		// Clear any partial output from the failed git attempt before
		// extracting the archive into the same directory.
		if cleanErr := resetDir(tmpDir); cleanErr != nil {
			return false, errors.Join(err, cleanErr)
		}
		if archiveErr := d.fetchArchive(ctx, tmpDir, s); archiveErr != nil {
			return false, errors.Join(err, archiveErr)
		}
		return false, nil
	}

	return false, err
}

// resetDir removes dir and recreates it empty.
func resetDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

// gitDownload clones the repo (and checks out d.ref if it is a commit hash)
// into tmpDir, then strips the .git directory and, if d.subdir is set, promotes
// that subdirectory's contents to the top level.
func (d *Downloader) gitDownload(ctx context.Context, tmpDir string, s settings.Settings) error {
	auth, err := d.resolveAuth(ctx, s)
	if err != nil {
		return fmt.Errorf("resolving git auth: %w", err)
	}

	depth := d.resolveDepth(s)

	cloneOpts := &git.CloneOptions{
		URL:  d.repoURL,
		Auth: auth,
	}

	if depth > 0 {
		cloneOpts.Depth = depth
	}

	// If we have a ref, set it as the reference to clone.
	if d.ref != "" {
		cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(d.ref)
		cloneOpts.SingleBranch = true
	}

	cloneDir := tmpDir
	if d.subdir != "" {
		cloneDir = filepath.Join(tmpDir, "_clone")
	}

	// Build the list of clone attempts based on the ref type.
	type cloneAttempt struct {
		refName      plumbing.ReferenceName
		singleBranch bool
	}

	var repo *git.Repository
	var attempts []cloneAttempt

	switch {
	case d.ref == "", looksLikeCommitHash(d.ref):
		// No ref or commit hash — clone the default branch.
		attempts = []cloneAttempt{{"", false}}
	default:
		// Named ref — try as branch, then as tag. No fallback to default
		// branch, so we error if the ref doesn't exist.
		attempts = []cloneAttempt{
			{plumbing.NewBranchReferenceName(d.ref), true},
			{plumbing.NewTagReferenceName(d.ref), true},
		}
	}

	for _, attempt := range attempts {
		os.RemoveAll(cloneDir)
		cloneOpts.ReferenceName = attempt.refName
		cloneOpts.SingleBranch = attempt.singleBranch
		repo, err = git.PlainCloneContext(ctx, cloneDir, false, cloneOpts)
		if err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("cloning repo: %w", err)
	}

	// If the ref is a commit hash or the fallback (no ref) was used with a non-branch/tag ref,
	// we need to checkout the specific ref after cloning.
	if d.ref != "" && looksLikeCommitHash(d.ref) {
		hash, err := resolveCommitHash(repo, d.ref)
		if err != nil {
			return fmt.Errorf("resolving commit %s: %w", d.ref, err)
		}
		wt, err := repo.Worktree()
		if err != nil {
			return err
		}
		if err := wt.Checkout(&git.CheckoutOptions{
			Hash: hash,
		}); err != nil {
			return fmt.Errorf("checking out commit %s: %w", d.ref, err)
		}
	}

	// Remove .git directory — we just want the content.
	os.RemoveAll(filepath.Join(cloneDir, ".git"))

	// If a subdir is requested, move its contents up to tmpDir.
	if d.subdir != "" {
		srcDir := filepath.Join(cloneDir, d.subdir)
		info, err := os.Stat(srcDir)
		if err != nil {
			return fmt.Errorf("subdirectory %q not found in repo: %w", d.subdir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("subdirectory %q is not a directory", d.subdir)
		}

		// Move contents from subdir to tmpDir.
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			src := filepath.Join(srcDir, e.Name())
			dst := filepath.Join(tmpDir, e.Name())
			if err := os.Rename(src, dst); err != nil {
				return fmt.Errorf("moving %s: %w", e.Name(), err)
			}
		}

		// Clean up clone dir.
		os.RemoveAll(cloneDir)
	}

	return nil
}

func (d *Downloader) resolveAuth(ctx context.Context, s settings.Settings) (transport.AuthMethod, error) {
	// Check for SSH URL.
	if strings.HasPrefix(d.repoURL, "ssh://") || scpPattern.MatchString(d.repoURL) {
		if len(s.Git.SSHKey) > 0 {
			keys, err := ssh.NewPublicKeys("git", s.Git.SSHKey, "")
			if err != nil {
				return nil, fmt.Errorf("parsing SSH key: %w", err)
			}
			if s.Git.InsecureSkipHostKeyVerify {
				keys.HostKeyCallback = cryptossh.InsecureIgnoreHostKey()
			}
			return keys, nil
		}
		// Fall back to SSH agent.
		auth, err := ssh.NewSSHAgentAuth("git")
		if err != nil {
			return nil, fmt.Errorf("SSH agent auth: %w", err)
		}
		if s.Git.InsecureSkipHostKeyVerify {
			auth.HostKeyCallback = cryptossh.InsecureIgnoreHostKey()
		}
		return auth, nil
	}

	// For HTTPS, check for embedded credentials in the URL.
	u, err := url.Parse(d.repoURL)
	if err == nil && u.User != nil {
		password, _ := u.User.Password()
		return &http.BasicAuth{
			Username: u.User.Username(),
			Password: password,
		}, nil
	}

	// Check configured HTTPS credentials.
	if cred := s.MatchHTTPSCredential(d.repoURL); cred != nil {
		return &http.BasicAuth{
			Username: cred.Username,
			Password: cred.Password,
		}, nil
	}

	// Try the system git credential helper (e.g. osxkeychain, manager-core).
	if u != nil && (u.Scheme == "https" || u.Scheme == "http") {
		if auth := gitCredentialFill(ctx, u.Scheme, u.Hostname()); auth != nil {
			return auth, nil
		}
	}

	return nil, nil
}

// gitCredentialFill shells out to "git credential fill" to resolve credentials
// from the user's configured credential helpers. Returns nil if git is not
// installed or no credentials are found.
func gitCredentialFill(ctx context.Context, protocol, host string) *http.BasicAuth {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil
	}

	cmd := exec.CommandContext(ctx, gitBin, "credential", "fill")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("protocol=%s\nhost=%s\n\n", protocol, host))

	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var username, password string
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "username":
			username = value
		case "password":
			password = value
		}
	}

	if username == "" && password == "" {
		return nil
	}

	return &http.BasicAuth{
		Username: username,
		Password: password,
	}
}

// sshToHTTPS converts SSH and SCP-style Git URLs to HTTPS.
// e.g. "git@github.com:user/repo.git" -> "https://github.com/user/repo.git"
// e.g. "ssh://git@github.com/user/repo.git" -> "https://github.com/user/repo.git"
// If the URL is already HTTPS or cannot be parsed, it is returned unchanged.
func sshToHTTPS(rawURL string) string {
	// SCP-style: git@github.com:user/repo.git
	if scpPattern.MatchString(rawURL) {
		// Find the @ and : to extract host and path.
		atIdx := strings.Index(rawURL, "@")
		colonIdx := strings.Index(rawURL[atIdx:], ":") + atIdx
		host := rawURL[atIdx+1 : colonIdx]
		path := rawURL[colonIdx+1:]
		return "https://" + host + "/" + path
	}

	// ssh:// scheme
	if strings.HasPrefix(rawURL, "ssh://") {
		u, err := url.Parse(rawURL)
		if err != nil {
			return rawURL
		}
		u.Scheme = "https"
		u.User = nil
		return u.String()
	}

	return rawURL
}

func (d *Downloader) resolveDepth(s settings.Settings) int {
	// URL param takes precedence.
	if d.depth > 0 {
		return d.depth
	}
	// Grabber-level config.
	if s.Git.Depth > 0 {
		return s.Git.Depth
	}
	// Commit hashes need full history so the commit is reachable.
	if looksLikeCommitHash(d.ref) {
		return 0
	}
	// For branches/tags/no-ref, default to depth 1. go-git is much slower
	// than system git for full clones and full history is rarely needed.
	return 1
}

// resolveCommitHash resolves a full or abbreviated commit hash to the full
// plumbing.Hash. For full 40-char hashes it does a direct lookup; for short
// hashes it walks the commit log and finds a commit whose hash starts with
// the given prefix (ambiguous matches return an error).
func resolveCommitHash(repo *git.Repository, ref string) (plumbing.Hash, error) {
	lower := strings.ToLower(ref)

	// Full hash — use directly.
	if len(ref) == 40 {
		h := plumbing.NewHash(lower)
		if _, err := repo.CommitObject(h); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("commit %s not found", ref)
		}
		return h, nil
	}

	// Short hash — iterate all commits and match prefix.
	iter, err := repo.Log(&git.LogOptions{All: true})
	if err != nil {
		return plumbing.ZeroHash, err
	}
	defer iter.Close()

	var match plumbing.Hash
	found := 0
	for {
		c, err := iter.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if strings.HasPrefix(c.Hash.String(), lower) {
			match = c.Hash
			found++
			if found > 1 {
				return plumbing.ZeroHash, fmt.Errorf("ambiguous short hash %s", ref)
			}
		}
	}
	if found == 0 {
		return plumbing.ZeroHash, fmt.Errorf("commit %s not found", ref)
	}
	return match, nil
}

func looksLikeCommitHash(ref string) bool {
	if len(ref) < 7 || len(ref) > 40 {
		return false
	}
	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
