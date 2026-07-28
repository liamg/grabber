package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"

	"github.com/liamg/grabber/settings"
)

// errSparseUnsupported signals that the remote could not serve a partial clone,
// so the caller should retry with a full clone. Errors that a full clone would
// hit just as surely (a missing subdirectory, a bad ref) are returned as-is.
var errSparseUnsupported = errors.New("remote does not support sparse checkout")

// sparseEligible reports whether this download can use the partial-clone path.
//
// This is not configurable. grabber returns files, never a usable repository,
// so downloading objects the caller cannot reach is never what anyone wants —
// the partial clone is used whenever it can be.
//
// A subdirectory is what makes it worthwhile. It is the only thing that narrows
// the set of files, and so the only thing that lets the blob filter skip
// anything. With no subdirectory every blob is needed regardless, and filtering
// them out only to request them all back costs an extra round trip for the same
// bytes. Fetching just the root files instead is not an option: it would
// silently drop every subdirectory, breaking a module at the repository root
// that refers to one (e.g. source = "./modules/vpc").
func (d *Downloader) sparseEligible() bool {
	return d.subdir != ""
}

// sparseDownload materialises only the requested directory into tmpDir, without
// downloading the rest of the repository's file contents.
//
// It works in three steps:
//
//  1. Clone with --filter=blob:none, which fetches commits and trees but no file
//     contents. The trees are what let us see the repository layout.
//  2. Walk the requested directory's tree and fetch exactly those blobs by hash.
//     go-git has no promisor/lazy-fetch engine (go-git#1381), so this stands in
//     for the fetch-on-demand the git binary would do during checkout.
//  3. Write the blobs straight to disk. go-git's own sparse checkout is not
//     usable here — against a partial clone it fails with "file not found" — and
//     we only want files on disk anyway, never a worktree or a .git directory.
func (d *Downloader) sparseDownload(ctx context.Context, tmpDir string, s settings.Settings, clientOpts []client.Option) error {
	// The bare clone is staged outside the destination so it can never collide
	// with a repository entry, and on the same filesystem as the rest of
	// grabber's staging.
	scratch, err := os.MkdirTemp(s.TemporaryDirectory, "grabber-sparse-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	repo, err := d.sparseClone(ctx, filepath.Join(scratch, "repo.git"), s, clientOpts)
	if err != nil {
		return err
	}

	commit, err := d.sparseCommit(ctx, repo, clientOpts)
	if err != nil {
		return err
	}

	sel, err := d.sparseEntries(repo, commit)
	if err != nil {
		return err
	}

	if sel.needsWorktree(s) {
		return fmt.Errorf("%w: %s needs a worktree to initialise",
			errSparseUnsupported, strings.Join(sel.submodules, ", "))
	}

	if err := fetchBlobs(ctx, repo, d.repoURL, sel.files, clientOpts); err != nil {
		return fmt.Errorf("%w: fetching %d objects: %w", errSparseUnsupported, len(sel.files), err)
	}

	return materialise(repo, sel.files, tmpDir)
}

// sparseClone makes a bare, blobless clone of the repo. Bare because the sparse
// path writes the working tree itself and never needs go-git's checkout.
func (d *Downloader) sparseClone(ctx context.Context, cloneDir string, s settings.Settings, clientOpts []client.Option) (*git.Repository, error) {
	cloneOpts := &git.CloneOptions{
		URL:           d.repoURL,
		ClientOptions: clientOpts,
		Bare:          true,
		NoCheckout:    true,
		Filter:        packp.FilterBlobNone(),
		Tags:          d.tagMode(),
	}
	// A pinned commit is fetched by hash after this clone (see
	// resolvePinnedCommit), so the clone only has to be a cheap anchor: one
	// commit of the default branch. The non-partial path cannot do that — it has
	// to find the commit locally — so it keeps full history and every ref.
	attempts := d.cloneAttempts()
	depth := d.resolveDepth(s)
	if looksLikeCommitHash(d.ref) {
		attempts = []cloneAttempt{{"", true}}
		depth = 1
		cloneOpts.Tags = plumbing.NoTags
	}
	if depth > 0 {
		cloneOpts.Depth = depth
	}

	var (
		repo *git.Repository
		err  error
	)
	for _, attempt := range attempts {
		_ = os.RemoveAll(cloneDir)
		cloneOpts.ReferenceName = attempt.refName
		cloneOpts.SingleBranch = attempt.singleBranch
		repo, err = git.PlainCloneContext(ctx, cloneDir, cloneOpts)
		if err == nil {
			return repo, nil
		}
	}
	// A remote that rejects the filter (or does not advertise protocol v2) fails
	// here; a full clone may still succeed.
	return nil, fmt.Errorf("%w: partial clone: %w", errSparseUnsupported, err)
}

// sparseCommit resolves the commit the download is pinned to, fetching whatever
// the initial shallow clone does not already contain.
//
// The clone is a single branch at depth 1, so a pinned commit is usually absent.
// Rather than deepen it wholesale, each question is answered with the cheapest
// object set that can answer it:
//
//   - A full hash is requested directly, with blob:none so the commit arrives
//     with its trees (which the file walk needs) but no file contents.
//   - A short hash has to be matched against the commit graph, and that needs no
//     trees at all — so history is deepened with tree:0, which sends commit
//     objects only. Once the full hash is known it is fetched as above.
//
// This mirrors what the go-getter fork did with "git fetch --unshallow
// --filter=tree:0" followed by "git fetch origin <sha> --depth 1".
func (d *Downloader) sparseCommit(ctx context.Context, repo *git.Repository, clientOpts []client.Option) (plumbing.Hash, error) {
	if d.ref != "" && looksLikeCommitHash(d.ref) {
		hash, err := d.resolvePinnedCommit(ctx, repo, clientOpts)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("resolving commit %s: %w", d.ref, err)
		}
		return hash, nil
	}
	head, err := repo.Head()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolving HEAD: %w", err)
	}
	return head.Hash(), nil
}

// resolvePinnedCommit returns the commit for a hash ref, fetching it (and, for a
// short hash, the commit graph needed to expand it) if the clone lacks it.
func (d *Downloader) resolvePinnedCommit(ctx context.Context, repo *git.Repository, clientOpts []client.Option) (plumbing.Hash, error) {
	// Already resolvable — a short hash whose commit happens to be in the clone,
	// or a full hash that is.
	if hash, err := resolveCommitHash(repo, d.ref); err == nil {
		if err := d.ensureCommitTrees(ctx, repo, hash, clientOpts); err != nil {
			return plumbing.ZeroHash, err
		}
		return hash, nil
	}

	if len(d.ref) < 40 {
		// A short hash cannot be requested by name, so deepen the history with
		// tree:0 — commit objects only, no trees or blobs — and match the prefix
		// against that graph.
		head, err := repo.Head()
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("resolving HEAD: %w", err)
		}
		if err := fetchObjects(ctx, repo, d.repoURL,
			[]plumbing.Hash{head.Hash()}, packp.FilterTreeDepth(0), 0, clientOpts); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("fetching commit graph: %w", err)
		}
	}

	hash, err := resolveCommitHash(repo, d.ref)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if err := d.ensureCommitTrees(ctx, repo, hash, clientOpts); err != nil {
		return plumbing.ZeroHash, err
	}
	return hash, nil
}

// ensureCommitTrees makes sure the commit's trees are present, fetching just
// that commit with blob:none when they are not. After a tree:0 deepen the commit
// object exists with no tree behind it, and the file walk needs the trees.
func (d *Downloader) ensureCommitTrees(ctx context.Context, repo *git.Repository, hash plumbing.Hash, clientOpts []client.Option) error {
	if c, err := object.GetCommit(repo.Storer, hash); err == nil {
		if _, err := c.Tree(); err == nil {
			return nil
		}
	}
	return fetchObjects(ctx, repo, d.repoURL,
		[]plumbing.Hash{hash}, packp.FilterBlobNone(), 1, clientOpts)
}

// sparseEntry is one file to write to disk, at its path within the repository.
// That path is used verbatim relative to the destination directory.
type sparseEntry struct {
	path string
	mode filemode.FileMode
	hash plumbing.Hash
}

// sparseSelection is what walking the tree for the requested directory turned up.
type sparseSelection struct {
	// files are the entries to write to disk.
	files []sparseEntry
	// submodules are the paths of any submodule entries within the selection.
	// A submodule's contents belong to another repository, so they cannot be
	// written from this object store.
	submodules []string
}

// needsWorktree reports whether this selection can only be completed by go-git's
// worktree path, so the caller must fall back to a full clone.
//
// A submodule's contents live in another repository and cannot be written from
// this object store; only the worktree path can clone and initialise them, and
// only when the caller asked for recursion. With recursion off a full clone would
// leave the directory empty too, so narrowing loses nothing and is kept.
func (sel sparseSelection) needsWorktree(s settings.Settings) bool {
	return len(sel.submodules) > 0 && s.Git.RecurseSubmodules
}

// add classifies one tree entry into the selection, ignoring anything that does
// not become a file on disk (directories, and modes git no longer produces).
func (sel *sparseSelection) add(p string, e object.TreeEntry) {
	switch {
	case e.Mode == filemode.Submodule:
		sel.submodules = append(sel.submodules, p)
	case materialisable(e.Mode):
		sel.files = append(sel.files, sparseEntry{path: p, mode: e.Mode, hash: e.Hash})
	}
}

// sparseEntries selects what to materialise, matching what
// "git sparse-checkout set <subdir>" leaves in a working tree under cone mode:
// the requested directory in full, plus the files sitting directly in each
// directory along the path to it (starting at the repository root). Directories
// off that path are excluded entirely. Every file keeps its repository-relative
// path, so several subdirectories can be merged into one destination tree.
//
// An empty subdir selects just the repository's root files. Callers reach this
// through sparseEligible, which requires a subdir, so in practice a download
// with no subdir takes the full-clone path instead.
func (d *Downloader) sparseEntries(repo *git.Repository, commit plumbing.Hash) (sparseSelection, error) {
	var sel sparseSelection

	c, err := object.GetCommit(repo.Storer, commit)
	if err != nil {
		return sel, fmt.Errorf("reading commit %s: %w", commit, err)
	}
	root, err := c.Tree()
	if err != nil {
		return sel, fmt.Errorf("reading tree for %s: %w", commit, err)
	}

	// The files sitting directly in each directory along the path to the
	// requested one, starting at the repository root.
	for _, dir := range ancestorDirs(d.subdir) {
		tree := root
		if dir != "" {
			tree, err = root.Tree(dir)
			if err != nil {
				return sel, fmt.Errorf("reading tree for %q: %w", dir, err)
			}
		}
		for _, e := range tree.Entries {
			sel.add(path.Join(dir, e.Name), e)
		}
	}

	if d.subdir == "" {
		return sel, nil
	}

	sub, err := root.Tree(d.subdir)
	if err != nil {
		// Not a partial-clone problem: a full clone would not find it either.
		return sel, fmt.Errorf("subdirectory %q not found in repo: %w", d.subdir, err)
	}

	walker := object.NewTreeWalker(sub, true, nil)
	defer walker.Close()
	for {
		name, e, err := walker.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sel, fmt.Errorf("walking %q: %w", d.subdir, err)
		}
		sel.add(path.Join(d.subdir, name), e)
	}

	return sel, nil
}

// ancestorDirs returns the directories along the path to subdir, from the
// repository root down to but excluding subdir itself. The root is always
// included, so an empty subdir yields just the root.
//
//	""      -> [""]
//	"a/b/c" -> ["", "a", "a/b"]
func ancestorDirs(subdir string) []string {
	dirs := []string{""}
	if subdir == "" {
		return dirs
	}
	parts := strings.Split(subdir, "/")
	for i := 1; i < len(parts); i++ {
		dirs = append(dirs, strings.Join(parts[:i], "/"))
	}
	return dirs
}

// materialisable reports whether a tree entry becomes a file on disk.
func materialisable(m filemode.FileMode) bool {
	return m == filemode.Regular || m == filemode.Executable || m == filemode.Symlink
}

// fetchBlobs downloads the given objects by hash and stores them in the repo.
//
// This is the step go-git cannot do on its own: a blob:none clone leaves the
// object store without file contents, and go-git has no promisor remote to fetch
// them on demand. Asking for objects by hash requires the server to allow
// arbitrary object IDs in "want" — the same requirement the git binary's lazy
// fetch has, so a remote that supports partial clone at all supports this.
func fetchBlobs(ctx context.Context, repo *git.Repository, rawURL string, entries []sparseEntry, clientOpts []client.Option) error {
	wants := make([]plumbing.Hash, 0, len(entries))
	seen := make(map[plumbing.Hash]struct{}, len(entries))
	for _, e := range entries {
		if _, ok := seen[e.hash]; ok {
			continue
		}
		seen[e.hash] = struct{}{}
		// Empty files and duplicated content may already be present.
		if _, err := repo.Storer.EncodedObject(plumbing.BlobObject, e.hash); err == nil {
			continue
		}
		wants = append(wants, e.hash)
	}
	if len(wants) == 0 {
		return nil
	}

	// No filter: these are the blobs themselves, and depth is irrelevant for
	// objects requested by hash.
	return fetchObjects(ctx, repo, rawURL, wants, "", 0, clientOpts)
}

// fetchObjects asks the remote for the given objects and stores whatever it
// sends in the repo.
//
// filter narrows what the server includes alongside the wants ("" for no
// filter); depth limits history (0 for no limit). Requesting objects by hash
// needs the server to allow arbitrary object IDs in "want" — the same
// requirement the git binary's lazy fetch has, so a remote that supports partial
// clone at all supports this.
func fetchObjects(
	ctx context.Context,
	repo *git.Repository,
	rawURL string,
	wants []plumbing.Hash,
	filter packp.Filter,
	depth int,
	clientOpts []client.Option,
) error {
	u, err := transport.ParseURL(rawURL)
	if err != nil {
		return err
	}

	cl := client.New(clientOpts...)
	// Protocol is deliberately left unset so the client negotiates its default
	// (v2, which is what carries object filters).
	sess, err := cl.Handshake(ctx, &transport.Request{
		URL:     u,
		Command: transport.UploadPackService,
	})
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	defer func() { _ = sess.Close() }()

	return sess.Fetch(ctx, repo.Storer, &transport.FetchRequest{
		Wants:  wants,
		Filter: filter,
		Depth:  depth,
	})
}

// materialise writes the entries into destRoot, creating parent directories as
// needed.
func materialise(repo *git.Repository, entries []sparseEntry, destRoot string) error {
	for _, e := range entries {
		dst, err := securePath(destRoot, e.path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := writeBlob(repo, e, dst); err != nil {
			if errors.Is(err, plumbing.ErrObjectNotFound) {
				// The remote accepted the request for this object but did not
				// deliver it, so it cannot really serve objects by hash. Treat it
				// like any other partial-clone shortfall and fall back to a full
				// clone rather than failing the download.
				return fmt.Errorf("%w: object for %s was not delivered: %w",
					errSparseUnsupported, e.path, err)
			}
			return fmt.Errorf("writing %s: %w", e.path, err)
		}
	}
	return nil
}

// writeBlob writes a single tree entry to dst, honouring its file mode.
func writeBlob(repo *git.Repository, e sparseEntry, dst string) error {
	blob, err := object.GetBlob(repo.Storer, e.hash)
	if err != nil {
		return err
	}
	rc, err := blob.Reader()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()

	if e.mode == filemode.Symlink {
		target, err := io.ReadAll(rc)
		if err != nil {
			return err
		}
		// A symlink may already exist from an earlier entry with the same path.
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return err
		}
		return os.Symlink(string(target), dst)
	}

	mode, err := e.mode.ToOSFileMode()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// securePath joins a repository-relative path onto root, rejecting anything that
// would escape it. Git itself forbids ".." and absolute paths in tree entries,
// but the destination is attacker-influenced data so it is checked here too.
func securePath(root, rel string) (string, error) {
	joined := filepath.Join(root, filepath.FromSlash(rel))
	cleanRoot := filepath.Clean(root)
	if joined != cleanRoot && !strings.HasPrefix(joined, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes the destination directory", rel)
	}
	return joined, nil
}
