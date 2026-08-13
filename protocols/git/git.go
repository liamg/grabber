package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	nethttp "net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/plumbing/transport/http"
	"github.com/go-git/go-git/v6/plumbing/transport/ssh"
	cryptossh "golang.org/x/crypto/ssh"

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
	return "git"
}

func (p *Protocol) Priority() int {
	return 90
}

// scpPattern matches SCP-style Git URLs like git@github.com:user/repo.git
var scpPattern = regexp.MustCompile(`^(?:[a-zA-Z0-9_]+)@[a-zA-Z0-9._-]+:`)

func (p *Protocol) Detect(rawURL string) (protocols.Downloadable, bool) {
	d, err := parseGitURL(rawURL, false)
	if err != nil {
		return nil, false
	}
	return d, true
}

// DetectForced accepts any URL it can parse, without the guesswork Detect needs.
// A "git::" prefix is the caller stating the remote is a Git repository, so a
// URL that neither ends in ".git" nor names one of the hosts we happen to know
// about - a self-hosted instance, typically - has to be accepted on their word.
func (p *Protocol) DetectForced(rawURL string) (protocols.Downloadable, bool) {
	d, err := parseGitURL(rawURL, true)
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
//   - https://github.com/user/repo.git?subdir=path/to/mod
//   - https://github.com/user/repo.git?ref=v1.0.0&depth=1
//   - ssh://git@github.com/user/repo.git
//   - git@github.com:user/repo.git
//   - github.com/user/repo (detected by known hosts)
//
// "//subdir" and "?subdir=" are two spellings of the same thing: both narrow the
// download to that directory's objects and both preserve the repository-relative
// layout, so the contents land at "<dest>/subdir". Keeping the layout is what
// lets a caller merge several subdirectories of one repository into a single
// destination tree. If both are given, "//subdir" wins.
//
// NOTE: all of the above formats can also include the prefix "git::" to help with detection, but the prefix is stripped before parsing.
//
// forced reports whether the caller used the "git::" prefix, which commits them
// to this protocol and so skips the is-this-a-Git-URL guesswork.
func parseGitURL(rawURL string, forced bool) (*Downloader, error) {
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

	if !forced && !isGitURL(u) {
		return nil, errors.New("not a Git URL")
	}

	// Extract subdir from // syntax.
	repoPath, subdir := splitSubdir(u.Path)

	// Extract query params.
	ref := u.Query().Get("ref")
	depth := parseDepth(u.Query().Get("depth"))
	subdir = resolveSubdir(subdir, u.Query().Get("subdir"))

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

// resolveSubdir picks between the "//subdir" and "?subdir=" spellings, which mean
// the same thing. The "//subdir" form wins when both are present. The result is
// cleaned and clamped to the repository, so it can never point outside it.
func resolveSubdir(pathSubdir, querySubdir string) string {
	if pathSubdir == "" {
		pathSubdir = querySubdir
	}
	return strings.Trim(path.Clean("/"+pathSubdir), "/")
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
	querySubdir := ""
	if queryStr != "" {
		q, err := url.ParseQuery(queryStr)
		if err != nil {
			return nil, err
		}
		ref = q.Get("ref")
		depth = parseDepth(q.Get("depth"))
		querySubdir = q.Get("subdir")
	}
	subdir = resolveSubdir(subdir, querySubdir)

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
	// subdir narrows the download to one directory of the repository. It stays at
	// its repository-relative path in the destination.
	subdir string
	ref    string
	depth  int
}

var _ protocols.Downloadable = (*Downloader)(nil)

func (d *Downloader) Download(ctx context.Context, tmpDir string, s settings.Settings) (bool, error) {
	// Try each candidate URL in turn (the URL as given, then a scheme fallback).
	candidates := d.cloneCandidates(s)

	var errs []error
	for i, cand := range candidates {
		if i > 0 {
			// Clear any partial output from the previous failed attempt.
			if cleanErr := resetDir(tmpDir); cleanErr != nil {
				return false, errors.Join(append(errs, cleanErr)...)
			}
		}

		// Fail fast (and fall back promptly) if the host is unreachable, rather
		// than waiting for the clone to time out.
		host, port := gitHostPort(cand)
		if probeErr := s.ProbeConnect(ctx, host, port); probeErr != nil {
			errs = append(errs, probeErr)
			continue
		}

		attempt := *d
		attempt.repoURL = cand
		if err := attempt.gitDownload(ctx, tmpDir, s); err != nil {
			errs = append(errs, err)
			// A definitive answer from the remote (the ref does not exist, the
			// repository is empty) is the same over every transport, so the
			// scheme fallback cannot change it — its failure would only bury
			// the real error under unrelated auth noise.
			if definitiveCloneError(err) {
				break
			}
			continue
		}
		return false, nil
	}

	// If git could not retrieve a commit hash, fall back to the hosting
	// platform's HTTP archive endpoint. This handles orphaned commits that are
	// unreachable via the git protocol but still downloadable via the API.
	if looksLikeCommitHash(d.ref) && ctx.Err() == nil {
		if cleanErr := resetDir(tmpDir); cleanErr != nil {
			return false, errors.Join(append(errs, cleanErr)...)
		}
		if archiveErr := d.fetchArchive(ctx, tmpDir, s); archiveErr != nil {
			return false, errors.Join(append(errs, archiveErr)...)
		}
		return false, nil
	}

	return false, errors.Join(errs...)
}

// definitiveCloneError reports whether err is an answer no retry can change:
// the remote was reached and said the ref does not exist or the repository is
// empty (true over every transport, so the scheme fallback cannot help), or the
// context is done (so no further attempt can run anyway). Auth and not-found
// errors are deliberately absent — hosts hide private repositories behind both,
// and authenticating over the other scheme is exactly what the fallback is for.
func definitiveCloneError(err error) bool {
	return errors.Is(err, git.ErrRemoteRefNotFound) ||
		errors.Is(err, transport.ErrEmptyRemoteRepository) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

// cloneCandidates returns the ordered list of repo URLs to try. The URL is
// attempted as given first; on failure the loop falls back to the other scheme:
//
//   - an SSH/SCP URL falls back to its HTTPS equivalent (always);
//   - an HTTPS/HTTP URL falls back to SSH only when an SSH key is configured for
//     the host (otherwise the fallback could not authenticate).
//
// WithGitSSHToHTTPS forces the HTTPS form up front with no SSH attempt.
func (d *Downloader) cloneCandidates(s settings.Settings) []string {
	orig := d.repoURL

	if s.Git.SSHToHTTPS {
		return []string{sshToHTTPS(orig)}
	}

	candidates := []string{orig}
	if strings.HasPrefix(orig, "ssh://") || scpPattern.MatchString(orig) {
		if https := sshToHTTPS(orig); https != orig {
			candidates = append(candidates, https)
		}
	} else if u, err := url.Parse(orig); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
		if s.MatchSSHKey(u.Hostname()) != nil {
			if sshURL := httpsToSSH(orig); sshURL != "" {
				candidates = append(candidates, sshURL)
			}
		}
	}
	return candidates
}

// gitHostPort returns the host and port a clone of rawURL would connect to,
// defaulting to 22 for SSH and 443/80 for HTTPS/HTTP. Returns empty strings for
// local paths (which need no probe).
func gitHostPort(rawURL string) (host, port string) {
	if scpPattern.MatchString(rawURL) {
		return sshHost(rawURL), "22"
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", ""
	}
	host = u.Hostname()
	if host == "" {
		return "", ""
	}
	port = u.Port()
	switch {
	case port != "":
	case u.Scheme == "ssh":
		port = "22"
	case u.Scheme == "https":
		port = "443"
	case u.Scheme == "http":
		port = "80"
	}
	return host, port
}

// resetDir removes dir and recreates it empty.
func resetDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

// gitDownload clones the repo into tmpDir, offering each candidate credential
// in turn until one is accepted.
//
// A remote that rejects the credential is the one failure another credential
// can fix, and a host commonly has several configured with only one of them
// still valid. Stopping at the first rejection lets a stale credential shadow a
// working one and fails the whole download; git does not do that, and neither
// does this.
func (d *Downloader) gitDownload(ctx context.Context, tmpDir string, s settings.Settings) error {
	// go-git dials directly (not through our transport), so apply the SSRF guard
	// as a pre-fetch check on the repo host.
	if err := s.CheckSSRFHost(ctx, gitURLHost(d.repoURL)); err != nil {
		return err
	}

	// The transport (CA bundle, client certificate, proxy, SSRF-guarded dialer)
	// does not depend on the credential, so it is built once and shared by every
	// attempt.
	transportOpts, err := d.transportOptions(s)
	if err != nil {
		return err
	}

	var errs []error
	attempted := false

	for _, candidate := range d.authCandidates(ctx, s) {
		auth, err := candidate.resolve()
		if err != nil {
			return fmt.Errorf("resolving git auth: %w", err)
		}
		if auth == nil {
			continue // this source had nothing to offer
		}
		authOpts, err := authOptions(auth)
		if err != nil {
			return fmt.Errorf("resolving git auth: %w", err)
		}

		if attempted {
			// Clear the partial clone left by the rejected credential.
			if resetErr := resetDir(tmpDir); resetErr != nil {
				return errors.Join(append(errs, resetErr)...)
			}
		}
		attempted = true

		err = d.cloneAndPrepare(ctx, tmpDir, s, append(authOpts, transportOpts...))
		if err == nil {
			return nil
		}
		errs = append(errs, err)
		// Only a rejected credential is worth retrying with another one.
		if !authFailure(err) {
			break
		}
	}

	last := len(errs) - 1
	switch {
	case !attempted:
		// No source supplied a credential, so the remote may well be public.
		return d.cloneAndPrepare(ctx, tmpDir, s, transportOpts)
	case last == 0:
		return errs[0]
	case authFailure(errs[last]):
		// Every credential was offered and every one came back rejected. The
		// individual 401s are identical, so the count is the useful part.
		return fmt.Errorf("tried %d credentials for this remote, all rejected; last error: %w",
			len(errs), errs[last])
	default:
		// A credential was rejected and then something else went wrong, so both
		// halves of the story matter.
		return errors.Join(errs...)
	}
}

// authFailure reports whether err is the remote rejecting the credential that
// was offered — the one failure a different credential could fix.
func authFailure(err error) bool {
	return errors.Is(err, transport.ErrAuthenticationRequired) ||
		errors.Is(err, transport.ErrAuthorizationFailed)
}

// cloneAndPrepare clones the repo (and checks out d.ref if it is a commit hash)
// into tmpDir, then strips the .git directory and, if d.subdir is set, checks
// that subdirectory exists.
func (d *Downloader) cloneAndPrepare(ctx context.Context, tmpDir string, s settings.Settings, clientOpts []client.Option) error {
	// Sparse checkout downloads only the objects backing the requested
	// directory. It falls through to a full clone when the remote cannot serve
	// a partial clone.
	if d.sparseEligible() {
		err := d.sparseDownload(ctx, tmpDir, s, clientOpts)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errSparseUnsupported) {
			return err
		}
		log.Printf("grabber: sparse checkout unavailable, falling back to a full clone: %v", err)
		if resetErr := resetDir(tmpDir); resetErr != nil {
			return resetErr
		}
	}

	depth := d.resolveDepth(s)

	cloneOpts := &git.CloneOptions{
		URL:           d.repoURL,
		ClientOptions: clientOpts,
		Tags:          d.tagMode(),
	}

	if depth > 0 {
		cloneOpts.Depth = depth
	}

	if s.Git.RecurseSubmodules {
		cloneOpts.RecurseSubmodules = git.DefaultSubmoduleRecursionDepth
	}

	cloneDir := tmpDir

	repo, err := cloneByAttempts(ctx, cloneDir, cloneOpts, d.cloneAttempts(), d.ref)
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

	if d.subdir == "" {
		return nil
	}

	// The subdir stays where it is; the whole repo is already at tmpDir. Check it
	// exists so a bad subdir is reported here rather than surfacing as a missing
	// file later.
	srcDir := filepath.Join(tmpDir, d.subdir)
	info, err := os.Stat(srcDir)
	if err != nil {
		return fmt.Errorf("subdirectory %q not found in repo: %w", d.subdir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("subdirectory %q is not a directory", d.subdir)
	}

	return nil
}

// cloneAttempt is one candidate reference to clone at.
type cloneAttempt struct {
	refName      plumbing.ReferenceName
	singleBranch bool
}

// cloneByAttempts clones at the first of the ordered reference candidates that
// exists on the remote. Only a missing ref moves on to the next spelling; any
// other failure (auth, transport, protocol) would fail identically for every
// spelling, so it is returned immediately. When every spelling is missing, the
// error names the ref the caller pinned rather than the internal spelling of
// whichever attempt happened to run last ("refs/tags/master" for ref=master).
func cloneByAttempts(ctx context.Context, cloneDir string, cloneOpts *git.CloneOptions, attempts []cloneAttempt, ref string) (*git.Repository, error) {
	var err error
	for _, attempt := range attempts {
		_ = os.RemoveAll(cloneDir)
		cloneOpts.ReferenceName = attempt.refName
		cloneOpts.SingleBranch = attempt.singleBranch
		var repo *git.Repository
		repo, err = git.PlainCloneContext(ctx, cloneDir, cloneOpts)
		if err == nil {
			return repo, nil
		}
		if !errors.Is(err, git.ErrRemoteRefNotFound) {
			return nil, err
		}
	}
	if len(attempts) > 1 {
		names := make([]string, len(attempts))
		for i, attempt := range attempts {
			names[i] = attempt.refName.String()
		}
		return nil, fmt.Errorf("%w %q (tried %s)", git.ErrRemoteRefNotFound, ref, strings.Join(names, ", "))
	}
	return nil, err
}

// cloneAttempts returns the ordered reference candidates to try for this
// download. A bare name is tried as a branch and then as a tag; a ref that
// already says which namespace it belongs to is used as-is; no ref clones just
// the default branch.
func (d *Downloader) cloneAttempts() []cloneAttempt {
	switch {
	case looksLikeCommitHash(d.ref):
		// A hash is not a ref name, so there is nothing to ask the server for by
		// name. Every ref is fetched so the commit can be found locally. The
		// partial-clone path narrows this (see sparseClone), because it can fetch
		// the commit by hash instead.
		return []cloneAttempt{{"", false}}
	case d.ref == "":
		return []cloneAttempt{{"", true}}
	default:
		if qualified, ok := qualifiedRefName(d.ref); ok {
			return []cloneAttempt{{qualified, true}}
		}
		// No fallback to the default branch, so we error if the ref doesn't exist.
		attempts := []cloneAttempt{
			{plumbing.NewBranchReferenceName(d.ref), true},
			{plumbing.NewTagReferenceName(d.ref), true},
		}
		// A name containing a slash may belong to a namespace other than heads
		// or tags — "notes/...", "pull/123/head", "remotes/origin/...". git
		// resolves those as refs/<name>, so try that too. It goes last because a
		// slash is far more often just a branch ("feature/foo"), and this costs a
		// clone attempt only on the path that was going to fail anyway.
		if strings.Contains(d.ref, "/") {
			attempts = append(attempts, cloneAttempt{plumbing.ReferenceName("refs/" + d.ref), true})
		}
		return attempts
	}
}

// qualifiedRefName expands a ref that already names its namespace into a full
// reference name, reporting false for a bare name the caller should resolve by
// searching.
//
// git resolves "tags/v1.0.0" and "refs/tags/v1.0.0" as readily as "v1.0.0", and
// module sources in the wild are pinned all three ways — "?ref=tags/0.19.2" is
// the convention across the cloudposse modules, for instance. Prefixing those
// unconditionally produces "refs/tags/tags/v1.0.0", which cannot resolve.
func qualifiedRefName(ref string) (plumbing.ReferenceName, bool) {
	switch {
	case strings.HasPrefix(ref, "refs/"):
		return plumbing.ReferenceName(ref), true
	case strings.HasPrefix(ref, "tags/"):
		return plumbing.NewTagReferenceName(strings.TrimPrefix(ref, "tags/")), true
	case strings.HasPrefix(ref, "heads/"):
		return plumbing.NewBranchReferenceName(strings.TrimPrefix(ref, "heads/")), true
	default:
		return "", false
	}
}

// tagMode reports which tags to fetch.
//
// grabber only ever hands back files. It strips .git and never runs another git
// operation, so tags are objects that get downloaded and then thrown away. This
// is not exposed as an option because no caller could make use of them, and in a
// repository with thousands of tags it is the single largest saving available —
// measured at ~5x on a mid-sized repo. A tag named as the ref is still fetched,
// because it is requested explicitly.
//
// A commit hash is the exception: the commit has to be found locally, and one
// reachable only from a tag would be unresolvable without them. The
// partial-clone path overrides this (see sparseClone), because it can fetch the
// commit by hash instead.
func (d *Downloader) tagMode() plumbing.TagMode {
	if looksLikeCommitHash(d.ref) {
		return plumbing.AllTags
	}
	return plumbing.NoTags
}

// transportOptions builds the non-authentication go-git client options for this
// download: for HTTP(S) remotes, the CA bundle, client certificate, proxy and
// SSRF-guarded dialer resolved from settings by host. The options are
// per-clone, so there is no global transport state to race on.
func (d *Downloader) transportOptions(s settings.Settings) ([]client.Option, error) {
	tr, err := httpTransportFor(d.repoURL, s)
	if err != nil {
		return nil, err
	}
	if tr == nil {
		return nil, nil
	}
	return []client.Option{client.WithHTTPClient(&nethttp.Client{Transport: tr})}, nil
}

// httpTransportFor builds the HTTP transport for an HTTP(S) remote, layering the
// configured CA pool, the client certificate matched to the host, the matched
// proxy (including any proxy credentials) and the SSRF dial guard. It returns
// nil for SSH/SCP remotes, which do not use an HTTP transport, and nil when
// nothing is configured so go-git keeps its own default client.
func httpTransportFor(repoURL string, s settings.Settings) (*nethttp.Transport, error) {
	if scpPattern.MatchString(repoURL) {
		return nil, nil
	}
	u, err := url.Parse(repoURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, nil
	}
	return s.TransportForHost(u.Hostname())
}

// credentialFillFunc is the system git credential helper, indirected through a
// package variable so tests can verify it is (or is not) consulted.
var credentialFillFunc = gitCredentialFill

// authOptions wraps a resolved credential as go-git client options.
func authOptions(auth any) ([]client.Option, error) {
	switch a := auth.(type) {
	case *ssh.PublicKeysCallback:
		return []client.Option{client.WithSSHAuth(newSSHAuth(a))}, nil
	case *http.BasicAuth:
		return []client.Option{client.WithHTTPAuth(a)}, nil
	default:
		return nil, fmt.Errorf("unsupported git auth type %T", auth)
	}
}

// authCandidate is one credential source in the ordered chain for a remote. The
// credential is resolved lazily, so a source with a side effect — the system git
// credential helper shells out to git — is only consulted once every preceding
// candidate has been tried and rejected.
type authCandidate struct {
	// resolve returns the credential, or a nil credential when this source has
	// nothing to offer and the chain should move on.
	resolve func() (any, error)
}

// authCandidates returns the ordered credential chain for the remote, yielding
// *ssh.PublicKeysCallback for SSH remotes and *http.BasicAuth for HTTP(S) ones.
// An empty chain (or one where every source declines) means anonymous access.
func (d *Downloader) authCandidates(ctx context.Context, s settings.Settings) []authCandidate {
	// SSH offers every identity in a single handshake and lets the server pick,
	// so there is nothing to fall back to: one candidate covers them all.
	if strings.HasPrefix(d.repoURL, "ssh://") || scpPattern.MatchString(d.repoURL) {
		return []authCandidate{{resolve: func() (any, error) {
			auth, err := d.resolveSSHAuth(s)
			if err != nil || auth == nil {
				// Returned untyped so callers can compare against nil.
				return nil, err
			}
			return auth, nil
		}}}
	}

	u, err := url.Parse(d.repoURL)
	urlUser := ""
	if err == nil && u.User != nil {
		// For HTTPS, a complete credential embedded in the URL wins outright:
		// the caller named the secret to use, so there is nothing to fall back
		// to.
		if password, ok := u.User.Password(); ok {
			username := u.User.Username()
			return []authCandidate{{resolve: func() (any, error) {
				return &http.BasicAuth{Username: username, Password: password}, nil
			}}}
		}
		// A username with no password is not a credential. It names the account
		// to authenticate as, and the password has to come from somewhere else.
		// Returning it as-is sends an empty password and earns a 401, with the
		// sources below never consulted.
		//
		// It does narrow those sources: only a credential for that account will
		// do, and the account is passed to the system helper so it can select
		// the right one. This is how git resolves a username in the URL.
		urlUser = u.User.Username()
	}

	var candidates []authCandidate

	// Every configured credential matching the remote, most specific first.
	for _, cred := range s.MatchHTTPSCredentials(d.repoURL) {
		candidates = append(candidates, authCandidate{resolve: func() (any, error) {
			return &http.BasicAuth{Username: cred.Username, Password: cred.Password}, nil
		}})
	}

	if u != nil {
		// The dynamic credential function, ahead of the system fallback.
		candidates = append(candidates, authCandidate{resolve: func() (any, error) {
			if user, pass, ok := s.RequestCredential(ctx, u.Scheme, u.Hostname(), u.Path); ok {
				return &http.BasicAuth{Username: user, Password: pass}, nil
			}
			return nil, nil
		}})

		// The system git credential helper (e.g. osxkeychain, manager-core).
		// This is a system fallback and is skipped when disabled.
		if !s.NoSystemFallback && (u.Scheme == "https" || u.Scheme == "http") {
			candidates = append(candidates, authCandidate{resolve: func() (any, error) {
				if auth := credentialFillFunc(ctx, u.Scheme, u.Hostname(), urlUser); auth != nil {
					return auth, nil
				}
				return nil, nil
			}})
		}
	}

	// Nothing supplied a password. Offer the URL's username alone, which some
	// hosts accept as a whole credential — a personal access token in the
	// username position, for instance.
	if urlUser != "" {
		candidates = append(candidates, authCandidate{resolve: func() (any, error) {
			return &http.BasicAuth{Username: urlUser}, nil
		}})
	}

	return candidates
}

// resolveAuth resolves the first credential the chain offers for the remote,
// returning *ssh.PublicKeysCallback for SSH remotes, *http.BasicAuth for HTTP(S)
// remotes, or nil for anonymous access. The download itself walks the whole
// chain (see gitDownload); this is the single-answer view of it.
func (d *Downloader) resolveAuth(ctx context.Context, s settings.Settings) (any, error) {
	for _, candidate := range d.authCandidates(ctx, s) {
		auth, err := candidate.resolve()
		if err != nil {
			return nil, err
		}
		if auth != nil {
			return auth, nil
		}
	}
	return nil, nil
}

// sshAuth adapts a go-git SSH auth method so that, when grabber supplies the
// host key policy, the client config also names the host key algorithms to
// negotiate.
//
// go-git derives those algorithms from known_hosts whenever the config leaves
// them unset, and a missing known_hosts file is fatal there — even with host
// key verification switched off, where the file would never be read. That
// lookup is independent of the host key callback, so a container with no
// ~/.ssh/known_hosts failed every SSH clone with "unable to find any valid
// known_hosts file, set SSH_KNOWN_HOSTS env variable", whatever key or
// host-key policy the caller configured, and the configured key never got
// offered. Naming the algorithms keeps that lookup from running; the list is
// the one x/crypto/ssh negotiates when the field is empty, so nothing else
// about the handshake changes.
type sshAuth struct {
	*ssh.PublicKeysCallback

	// ownHostKeyPolicy records that grabber decided how host keys are verified,
	// so go-git has no reason to read known_hosts at all. Without it go-git is
	// verifying against the user's own ~/.ssh/known_hosts, where the algorithms
	// recorded for the host are the stricter choice and are left to go-git.
	ownHostKeyPolicy bool
}

var _ client.SSHAuth = sshAuth{}

// newSSHAuth wraps auth, noting whether it carries a host key policy of our
// own. It must be read here rather than in ClientConfig, which is where go-git
// fills a nil callback in with its known_hosts default.
func newSSHAuth(auth *ssh.PublicKeysCallback) sshAuth {
	return sshAuth{
		PublicKeysCallback: auth,
		ownHostKeyPolicy:   auth.HostKeyCallback != nil,
	}
}

func (a sshAuth) ClientConfig(ctx context.Context, req *transport.Request) (*cryptossh.ClientConfig, error) {
	cfg, err := a.PublicKeysCallback.ClientConfig(ctx, req)
	if err != nil {
		return nil, err
	}
	if a.ownHostKeyPolicy && len(cfg.HostKeyAlgorithms) == 0 {
		cfg.HostKeyAlgorithms = cryptossh.SupportedAlgorithms().HostKeys
	}
	return cfg, nil
}

// resolveSSHAuth builds the SSH authentication method. A configured key and the
// SSH agent (when the system fallback is enabled) are combined into a single
// PublicKeysCallback that offers the configured key first, then the agent's
// identities. This mirrors ssh: all identities are offered in one handshake and
// the server picks, so a key the server rejects transparently falls through to
// the agent - without go-git's one-AuthMethod-per-clone limit forcing a retry.
// Returns nil (anonymous) when nothing is available.
func (d *Downloader) resolveSSHAuth(s settings.Settings) (*ssh.PublicKeysCallback, error) {
	hostKeyCallback, err := sshHostKeyCallback(s, sshHost(d.repoURL))
	if err != nil {
		return nil, err
	}

	// Parse every applicable configured key (host-specific first, then defaults)
	// up front; these are offered before the agent.
	var staticSigners []cryptossh.Signer
	for _, key := range s.MatchSSHKeys(sshHost(d.repoURL)) {
		signer, err := cryptossh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parsing SSH key: %w", err)
		}
		staticSigners = append(staticSigners, signer)
	}

	// The SSH agent is a system fallback, resolved lazily at handshake time. A
	// missing agent is only an error when there is no configured key to offer
	// instead.
	var agentSigners func() ([]cryptossh.Signer, error)
	if !s.NoSystemFallback {
		agent, err := ssh.NewSSHAgentAuth("git")
		switch {
		case err != nil && len(staticSigners) == 0:
			return nil, fmt.Errorf("SSH agent auth: %w", err)
		case err == nil:
			agentSigners = agent.Callback
		}
	}

	if len(staticSigners) == 0 && agentSigners == nil {
		return nil, nil // anonymous
	}

	// A single PublicKeysCallback offers every identity - the configured keys
	// followed by the agent's - in one handshake, so the server picks a usable
	// one (mirroring ssh) without go-git's one-AuthMethod-per-clone limit forcing
	// a retry.
	auth := &ssh.PublicKeysCallback{
		User: "git",
		Callback: func() ([]cryptossh.Signer, error) {
			signers := append([]cryptossh.Signer(nil), staticSigners...)
			if agentSigners != nil {
				// A failing agent (e.g. it went away) is skipped so the configured
				// keys are still offered.
				if got, err := agentSigners(); err == nil {
					signers = append(signers, got...)
				}
			}
			return signers, nil
		},
	}
	if hostKeyCallback != nil {
		auth.HostKeyCallback = hostKeyCallback
	}
	return auth, nil
}

// sshHostKeyCallback selects the SSH host-key verification strategy. A nil
// callback (with nil error) means "leave go-git's default", which reads
// ~/.ssh/known_hosts. Precedence:
//
//   - InsecureSkipHostKeyVerify → accept any key.
//   - KnownHosts configured → verify in memory (allow unknown, reject changed).
//   - system fallback enabled → nil (go-git reads ~/.ssh/known_hosts).
//   - otherwise (no known hosts, no disk access) → accept any key, with a warning.
func sshHostKeyCallback(s settings.Settings, host string) (cryptossh.HostKeyCallback, error) {
	switch {
	case s.Git.InsecureSkipHostKeyVerify:
		return cryptossh.InsecureIgnoreHostKey(), nil
	case len(s.Git.KnownHosts) > 0:
		return knownHostsCallback(s.Git.KnownHosts)
	case !s.NoSystemFallback:
		return nil, nil
	default:
		log.Printf("grabber: SSH host key verification disabled for %q "+
			"(no known_hosts configured and system fallback is off); accepting any host key", host)
		return cryptossh.InsecureIgnoreHostKey(), nil
	}
}

// knownHostsCallback builds an in-memory ssh.HostKeyCallback from known_hosts
// data. Unknown hosts are allowed; a host recorded with a different key is
// rejected (detecting key changes / potential MITM). Hashed host entries
// (|1|...) are not supported and are skipped.
func knownHostsCallback(knownHosts []byte) (cryptossh.HostKeyCallback, error) {
	hostKeys := map[string][]cryptossh.PublicKey{}

	rest := knownHosts
	for len(rest) > 0 {
		_, hosts, pubKey, _, remaining, err := cryptossh.ParseKnownHosts(rest)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing known_hosts: %w", err)
		}
		rest = remaining
		for _, h := range hosts {
			if strings.HasPrefix(h, "|") {
				continue // hashed host entry — unsupported
			}
			key := normalizeKnownHost(h)
			hostKeys[key] = append(hostKeys[key], pubKey)
		}
	}

	return func(hostname string, _ net.Addr, key cryptossh.PublicKey) error {
		keys := hostKeys[normalizeKnownHost(hostname)]
		if len(keys) == 0 {
			return nil // unknown host — allow
		}
		marshaled := key.Marshal()
		for _, known := range keys {
			if bytes.Equal(known.Marshal(), marshaled) {
				return nil
			}
		}
		return fmt.Errorf("ssh: host key mismatch for %q: the recorded key has changed", hostname)
	}, nil
}

// normalizeKnownHost reduces a known_hosts host pattern or a dialed hostname to
// a bare, lowercased host so the two can be compared. It strips any port and
// [ ] brackets (e.g. "[example.com]:2222" and "example.com:22" → "example.com").
func normalizeKnownHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	return h
}

// gitCredentialFill shells out to "git credential fill" to resolve credentials
// from the user's configured credential helpers. Returns nil if git is not
// installed or no credentials are found.
func gitCredentialFill(ctx context.Context, protocol, host, username string) *http.BasicAuth {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil
	}

	// A known username narrows the lookup to that account, which is what git
	// does with a username in the URL. Without it a helper holding credentials
	// for several accounts on one host can hand back the wrong one.
	query := fmt.Sprintf("protocol=%s\nhost=%s\n", protocol, host)
	if username != "" {
		query += fmt.Sprintf("username=%s\n", username)
	}

	cmd := exec.CommandContext(ctx, gitBin, "credential", "fill")
	cmd.Stdin = strings.NewReader(query + "\n")
	// Configured helpers (keychain, manager-core, ...) are still consulted; what
	// this suppresses is git falling back to asking a human. A library call must
	// not be able to block on a terminal, and in a runner there is no terminal to
	// answer it.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var gotUser, gotPass string
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "username":
			gotUser = value
		case "password":
			gotPass = value
		}
	}

	if gotUser == "" && gotPass == "" {
		return nil
	}

	return &http.BasicAuth{
		Username: gotUser,
		Password: gotPass,
	}
}

// gitURLHost extracts the network host from any Git URL form (ssh, scp, https).
// Returns "" when there is no network host (e.g. a local path).
func gitURLHost(rawURL string) string {
	if strings.HasPrefix(rawURL, "ssh://") || scpPattern.MatchString(rawURL) {
		return sshHost(rawURL)
	}
	if u, err := url.Parse(rawURL); err == nil {
		return u.Hostname()
	}
	return ""
}

// sshHost extracts the hostname from an SSH or SCP-style Git URL.
// e.g. "git@github.com:user/repo.git" -> "github.com"
// e.g. "ssh://git@github.com:22/user/repo.git" -> "github.com"
// Returns "" if the host cannot be determined.
func sshHost(rawURL string) string {
	// SCP-style: git@github.com:user/repo.git
	if scpPattern.MatchString(rawURL) {
		atIdx := strings.Index(rawURL, "@")
		colonIdx := strings.Index(rawURL[atIdx:], ":") + atIdx
		return rawURL[atIdx+1 : colonIdx]
	}

	// ssh:// scheme
	if u, err := url.Parse(rawURL); err == nil {
		return u.Hostname()
	}

	return ""
}

// httpsToSSH converts an HTTPS Git URL to its SSH equivalent (the reverse of
// sshToHTTPS), assuming the "git" SSH user. It handles the Azure DevOps special
// case, where the SSH form differs structurally from HTTPS. Returns "" if the
// URL cannot be converted.
//
// e.g. "https://github.com/user/repo.git" -> "ssh://git@github.com/user/repo.git"
// e.g. "https://dev.azure.com/org/proj/_git/repo" -> "ssh://git@ssh.dev.azure.com/v3/org/proj/repo"
func httpsToSSH(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	path := strings.TrimPrefix(u.Path, "/")

	if u.Hostname() == "dev.azure.com" {
		// https://dev.azure.com/org/project/_git/repo
		parts := strings.Split(path, "/")
		if len(parts) >= 4 && parts[2] == "_git" {
			return fmt.Sprintf("ssh://git@ssh.dev.azure.com/v3/%s/%s/%s", parts[0], parts[1], parts[3])
		}
		return ""
	}

	path = strings.TrimSuffix(path, ".git")
	if path == "" {
		return ""
	}
	return fmt.Sprintf("ssh://git@%s/%s.git", u.Hostname(), path)
}

// sshToHTTPS converts SSH and SCP-style Git URLs to HTTPS.
// e.g. "git@github.com:user/repo.git" -> "https://github.com/user/repo.git"
// e.g. "ssh://git@github.com/user/repo.git" -> "https://github.com/user/repo.git"
// e.g. "ssh://git@ssh.dev.azure.com/v3/org/proj/repo" -> "https://dev.azure.com/org/proj/_git/repo"
// If the URL is already HTTPS or cannot be parsed, it is returned unchanged.
func sshToHTTPS(rawURL string) string {
	var https string

	switch {
	// SCP-style: git@github.com:user/repo.git
	case scpPattern.MatchString(rawURL):
		// Find the @ and : to extract host and path.
		atIdx := strings.Index(rawURL, "@")
		colonIdx := strings.Index(rawURL[atIdx:], ":") + atIdx
		host := rawURL[atIdx+1 : colonIdx]
		path := rawURL[colonIdx+1:]
		https = "https://" + host + "/" + path

	// ssh:// scheme
	case strings.HasPrefix(rawURL, "ssh://"):
		u, err := url.Parse(rawURL)
		if err != nil {
			return rawURL
		}
		u.Scheme = "https"
		u.User = nil
		https = u.String()

	default:
		return rawURL
	}

	// Azure DevOps needs more than a scheme swap, so give it the chance to
	// rewrite the result before it is returned.
	if azure := azureDevOpsHTTPS(https); azure != "" {
		return azure
	}
	return https
}

// azureDevOpsHTTPS rewrites the plain scheme swap of an Azure DevOps SSH remote
// onto the host and path layout that actually serves Git over HTTPS. It is the
// inverse of the Azure DevOps case in httpsToSSH: the SSH form lives on
// ssh.dev.azure.com beneath a "v3" prefix, while the HTTPS form lives on
// dev.azure.com and introduces the repository with a "_git" segment. Swapping
// only the scheme leaves a host that does not serve Git over HTTP at all.
//
// e.g. "https://ssh.dev.azure.com/v3/org/proj/repo" -> "https://dev.azure.com/org/proj/_git/repo"
//
// It returns "" for any other host, leaving the scheme swap to stand.
func azureDevOpsHTTPS(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() != "ssh.dev.azure.com" {
		return ""
	}

	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "v3" {
		return ""
	}

	u.Host = "dev.azure.com"
	u.Path = fmt.Sprintf("/%s/%s/_git/%s", parts[1], parts[2], parts[3])
	return u.String()
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
	// Commit hashes need history so the commit is reachable locally. The
	// partial-clone path overrides this (see sparseClone), because it can fetch
	// the commit by hash instead of searching for it.
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
