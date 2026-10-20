package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/format/pktline"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"

	"github.com/liamg/grabber/settings"
)

// pinnedCommitCloneFunc is pinnedCommitClone, indirected through a package
// variable so tests can verify the path is taken, or force it to fail.
var pinnedCommitCloneFunc = (*Downloader).pinnedCommitClone

// fetchObjectsFunc is fetchObjects, indirected through a package variable so
// tests can stand in for how a server refuses a want.
var fetchObjectsFunc = fetchObjects

// errPinnedFetchUnsupported signals that the remote could not serve the pinned
// commit by hash, so the caller should fall back to a full clone. Errors that
// a full clone would hit just as surely (a missing commit, a rejected
// credential, a cancelled context) are returned without it.
var errPinnedFetchUnsupported = errors.New("remote could not serve the commit by hash")

// pinnedCommitEligible reports whether this download can fetch its commit by
// hash rather than cloning the repository to find it.
//
// Only a full hash qualifies. A short hash cannot be requested from the remote,
// so it has to be matched against commits that are already local, which is what
// the full clone is for. The partial-clone path (sparseCommit) handles short
// hashes itself, so it is unaffected.
func (d *Downloader) pinnedCommitEligible() bool {
	return len(d.ref) == 40 && looksLikeCommitHash(d.ref)
}

// pinnedCommitClone creates a repository in cloneDir that holds only the pinned
// commit, with all of its trees and blobs. This is the go-git equivalent of:
//
//	git init && git fetch origin <sha> --depth 1
//
// The ordinary clone has to download every ref and the whole history to be
// sure of finding the commit locally. In a large repository that costs
// gigabytes of memory, all of it thrown away, because grabber strips .git
// anyway.
//
// The worktree is left empty. The caller checks the commit out.
func (d *Downloader) pinnedCommitClone(ctx context.Context, cloneDir string, s settings.Settings, clientOpts []client.Option) (*git.Repository, error) {
	repo, err := git.PlainInit(cloneDir, false)
	if err != nil {
		return nil, err
	}

	// Submodule URLs are commonly relative ("../sub.git"), and go-git resolves
	// them against the origin remote, so it has to exist just as it would after
	// a clone.
	if _, err := repo.CreateRemote(&config.RemoteConfig{
		Name: git.DefaultRemoteName,
		URLs: []string{d.repoURL},
	}); err != nil {
		return nil, err
	}

	// Only the one commit is needed, but an explicitly requested depth is
	// honoured, as it is for every other kind of ref.
	depth := 1
	if d.depth > 0 || s.Git.Depth > 0 {
		depth = d.resolveDepth(s)
	}

	hash := plumbing.NewHash(d.ref)
	if err := fetchCommitByHash(ctx, repo, d.repoURL, hash, "", depth, clientOpts); err != nil {
		// A rejected credential or a finished context fails the full clone too.
		if authFailure(err) || ctx.Err() != nil {
			return nil, err
		}
		// A server that only serves advertised commits refuses this one whether
		// or not it has it, however it phrases the refusal, so the full clone is
		// the only way to find out.
		if errors.Is(err, errWantsRestricted) {
			return nil, fmt.Errorf("%w: %w", errPinnedFetchUnsupported, err)
		}
		if errors.Is(err, git.ErrRemoteRefNotFound) || definitiveCloneError(err) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", errPinnedFetchUnsupported, err)
	}

	// A server that sends a partial pack would otherwise fail the checkout.
	// Check every object the checkout needs is present, and fall back if not.
	if err := commitComplete(repo, hash); err != nil {
		return nil, fmt.Errorf("%w: incomplete pack: %w", errPinnedFetchUnsupported, err)
	}

	return repo, nil
}

// fetchCommitByHash fetches a single commit by hash into repo, reporting a
// commit the remote does not have as git.ErrRemoteRefNotFound so it reads the
// same as a missing named ref.
//
// A server that has the commit serves it over protocol v2, which fetchObjects
// requests. A server that does not answers "not our ref", or sends nothing,
// which go-git reads as an empty repository.
func fetchCommitByHash(
	ctx context.Context,
	repo *git.Repository,
	rawURL string,
	hash plumbing.Hash,
	filter packp.Filter,
	depth int,
	clientOpts []client.Option,
) error {
	err := fetchObjectsFunc(ctx, repo, rawURL, []plumbing.Hash{hash}, filter, depth, clientOpts)
	if err == nil {
		return nil
	}
	if commitMissingOnRemote(err) {
		return fmt.Errorf("%w: commit %s", git.ErrRemoteRefNotFound, hash)
	}
	return err
}

// commitMissingOnRemote reports whether err is the remote saying it does not
// have the requested commit. That is only certain from a server that serves any
// reachable commit by hash; any other refuses commits it has, too.
func commitMissingOnRemote(err error) bool {
	if errors.Is(err, errWantsRestricted) {
		return false
	}
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return true
	}
	var line *pktline.ErrorLine
	if errors.As(err, &line) && strings.Contains(line.Text, "not our ref") {
		return true
	}
	var remote *transport.RemoteError
	return errors.As(err, &remote) && strings.Contains(remote.Reason, "not our ref")
}

// commitComplete reports whether the commit, its trees, and every blob a
// checkout would write are present in repo. Blobs are only checked for
// presence, not read.
func commitComplete(repo *git.Repository, hash plumbing.Hash) error {
	c, err := object.GetCommit(repo.Storer, hash)
	if err != nil {
		return fmt.Errorf("commit %s: %w", hash, err)
	}
	tree, err := c.Tree()
	if err != nil {
		return fmt.Errorf("tree for %s: %w", hash, err)
	}
	walker := object.NewTreeWalker(tree, true, nil)
	defer walker.Close()
	for {
		name, e, err := walker.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if !materialisable(e.Mode) {
			continue
		}
		if err := repo.Storer.HasEncodedObject(e.Hash); err != nil {
			return fmt.Errorf("blob for %s: %w", name, err)
		}
	}
}
