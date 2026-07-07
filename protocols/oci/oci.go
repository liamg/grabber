package oci

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/liamg/grabber/protocols"
	"github.com/liamg/grabber/settings"
)

type Protocol struct{}

var _ protocols.Protocol = (*Protocol)(nil)

func New() *Protocol {
	return &Protocol{}
}

func (p *Protocol) Prefix() string {
	return "oci"
}

func (p *Protocol) Priority() int {
	return 50
}

func (p *Protocol) Detect(rawURL string) (protocols.Downloadable, bool) {
	d, err := parseOCIURL(rawURL)
	if err != nil {
		return nil, false
	}
	return d, true
}

// parseOCIURL parses an OCI registry URL.
//
// Supported formats:
//   - oci://registry.example.com/repo:tag
//   - oci://registry.example.com/repo@sha256:digest
//   - oci://registry.example.com/repo (defaults to :latest)
func parseOCIURL(rawURL string) (*Downloader, error) {
	// Must use oci:// scheme for auto-detection.
	if !strings.HasPrefix(rawURL, "oci://") {
		return nil, errors.New("not an OCI URL")
	}

	// Strip the scheme for parsing.
	remainder := strings.TrimPrefix(rawURL, "oci://")

	// Parse as a URL to extract the host and path.
	u, err := url.Parse("https://" + remainder)
	if err != nil {
		return nil, err
	}

	if u.Host == "" {
		return nil, errors.New("no registry host specified")
	}

	repo := strings.TrimPrefix(u.Path, "/")
	if repo == "" {
		return nil, errors.New("no repository specified")
	}

	// The reference (tag or digest) may be part of the repo string.
	// oras handles "repo:tag" and "repo@sha256:..." natively.
	ref := u.Host + "/" + repo

	return &Downloader{
		ref:      ref,
		registry: u.Host,
	}, nil
}

// ociRepoPath returns the repository portion of a full reference
// ("registry/repo:tag" → "repo"), stripping the registry prefix and any
// tag/digest, for use as the path passed to a dynamic credential function.
func ociRepoPath(ref, registry string) string {
	repo := strings.TrimPrefix(ref, registry)
	repo = strings.TrimPrefix(repo, "/")
	if i := strings.IndexAny(repo, ":@"); i >= 0 {
		repo = repo[:i]
	}
	return repo
}

type Downloader struct {
	ref      string // full reference: registry/repo:tag
	registry string // registry host
}

var _ protocols.Downloadable = (*Downloader)(nil)

func (d *Downloader) Download(ctx context.Context, tmpDir string, s settings.Settings) (bool, error) {
	repo, err := remote.NewRepository(d.ref)
	if err != nil {
		return false, fmt.Errorf("creating OCI repository: %w", err)
	}

	// Route the registry client through the configured transport for this host
	// (CA pool, client cert, proxy and/or a caller-supplied SSRF-guarded base
	// transport) and/or the credential matched for this registry. When neither
	// is set we leave repo.Client nil so oras uses its own default client.
	//
	// The retry policy is layered on top of the transport via retry.NewTransport,
	// so oras's default retry-on-429/5xx behaviour is preserved and each retried
	// attempt is re-dialed through the guard. A nil transport falls back to
	// http.DefaultTransport inside retry.
	tr, err := s.TransportForHost(d.registry)
	if err != nil {
		return false, fmt.Errorf("configuring OCI transport: %w", err)
	}
	// Resolve credentials: a static registry credential wins; otherwise the
	// dynamic request function.
	var username, password string
	hasCreds := false
	if cred := s.MatchOCICredential(d.registry); cred != nil && (cred.Username != "" || cred.Password != "") {
		username, password, hasCreds = cred.Username, cred.Password, true
	} else if user, pass, ok := s.RequestCredential(ctx, "oci", d.registry, ociRepoPath(d.ref, d.registry)); ok {
		username, password, hasCreds = user, pass, true
	}

	if tr != nil || hasCreds {
		// Pass an interface-typed nil (not a typed-nil *http.Transport) when
		// there is no custom transport, so retry falls back to its default.
		var base http.RoundTripper
		if tr != nil {
			base = tr
		}
		authClient := &auth.Client{
			Client: &http.Client{Transport: retry.NewTransport(base)},
			Cache:  auth.NewCache(),
		}
		if hasCreds {
			authClient.Credential = auth.StaticCredential(d.registry, auth.Credential{
				Username: username,
				Password: password,
			})
		}
		repo.Client = authClient
	}

	if s.OCIPlainHTTP {
		repo.PlainHTTP = true
	}

	// Create a file store to pull content into.
	store, err := file.New(tmpDir)
	if err != nil {
		return false, fmt.Errorf("creating file store: %w", err)
	}
	defer store.Close()

	// Copy (pull) the artifact from the remote registry.
	desc, err := oras.Copy(ctx, repo, d.ref, store, d.ref, oras.DefaultCopyOptions)
	if err != nil {
		return false, fmt.Errorf("pulling OCI artifact %s: %w", d.ref, err)
	}

	// If the manifest is a single blob/file, report as file.
	isFile := desc.MediaType != v1.MediaTypeImageManifest && desc.MediaType != v1.MediaTypeImageIndex

	// If only one file was pulled, report as file for archive extraction.
	if !isFile {
		entries, err := os.ReadDir(tmpDir)
		if err == nil && len(entries) == 1 && !entries[0].IsDir() {
			isFile = true
		}
	}

	// Clean up oras metadata files if present.
	cleanupOrasMetadata(tmpDir)

	return isFile, nil
}

// cleanupOrasMetadata removes oras internal files that shouldn't be in the output.
func cleanupOrasMetadata(dir string) {
	for _, name := range []string{"oci-layout", "index.json"} {
		os.Remove(filepath.Join(dir, name))
	}
	// Remove blobs directory if it exists and is empty.
	blobsDir := filepath.Join(dir, "blobs")
	if entries, err := os.ReadDir(blobsDir); err == nil && len(entries) == 0 {
		os.Remove(blobsDir)
	}
}
