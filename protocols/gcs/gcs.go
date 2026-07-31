package gcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/storage/v1"

	"github.com/liamg/grabber/internal/limitio"
	"github.com/liamg/grabber/internal/safepath"
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
	return "gcs"
}

func (p *Protocol) Priority() int {
	return 60
}

func (p *Protocol) Detect(rawURL string) (protocols.Downloadable, bool) {
	d, err := parseGCSURL(rawURL, false)
	if err != nil {
		return nil, false
	}
	return d, true
}

// DetectForced accepts a host that is not one of Google's own, reading it as a
// storage-compatible endpoint. A "gcs::" prefix is the caller stating the remote
// speaks the GCS API, which an emulator or a private endpoint does without
// carrying a googleapis.com name.
func (p *Protocol) DetectForced(rawURL string) (protocols.Downloadable, bool) {
	d, err := parseGCSURL(rawURL, true)
	if err != nil {
		return nil, false
	}
	return d, true
}

// parseGCSURL parses a GCS URL and returns a Downloader.
//
// Supported formats:
//   - storage.googleapis.com/bucket/key
//   - storage.cloud.google.com/bucket/key
//   - bucket.storage.googleapis.com/key
//   - https://custom-host/bucket/key            (custom endpoint, forced only)
//
// forced reports whether the caller used the "gcs::" prefix. It commits them to
// this protocol, so a host that is none of the above is read as a custom
// endpoint (path-style, bucket = first path segment) rather than rejected.
func parseGCSURL(rawURL string, forced bool) (*Downloader, error) {
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	host := strings.ToLower(u.Hostname())

	var bucket, key, endpoint string

	switch {
	case host == "storage.googleapis.com" || host == "storage.cloud.google.com":
		// Path-style: storage.googleapis.com/bucket/key
		bucket, key = splitBucketKey(u.Path)
	case strings.HasSuffix(host, ".storage.googleapis.com"):
		// Virtual-hosted: bucket.storage.googleapis.com/key
		bucket = strings.TrimSuffix(host, ".storage.googleapis.com")
		key = strings.TrimPrefix(u.Path, "/")
	case forced:
		// The host names the endpoint to talk to, so it is carried through
		// rather than dropped: a request built from the bucket alone would go to
		// Google, which is not where the caller pointed us.
		bucket, key = splitBucketKey(u.Path)
		endpoint = u.Scheme + "://" + u.Host
	default:
		return nil, errors.New("not a GCS URL")
	}

	if bucket == "" {
		return nil, errors.New("no bucket specified")
	}

	return &Downloader{
		bucket:   bucket,
		key:      key,
		endpoint: endpoint,
	}, nil
}

// splitBucketKey splits a path-style "/bucket/key" into its two parts. The key
// is empty when the path names only a bucket.
func splitBucketKey(path string) (string, string) {
	path = strings.TrimPrefix(path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	return bucket, key
}

type Downloader struct {
	bucket string
	key    string
	// endpoint is the storage-compatible host named by a forced URL, empty for
	// Google's own. It takes precedence over the configured endpoint, being the
	// more specific of the two.
	endpoint string
}

var _ protocols.Downloadable = (*Downloader)(nil)

func (d *Downloader) Download(ctx context.Context, tmpDir string, s settings.Settings) (bool, error) {
	svc, err := d.newService(ctx, s)
	if err != nil {
		return false, fmt.Errorf("creating GCS client: %w", err)
	}

	// If the key ends with "/" or is empty, treat as a directory listing.
	if d.key == "" || strings.HasSuffix(d.key, "/") {
		return false, d.downloadDir(ctx, svc, tmpDir, s.MaxBytes)
	}
	return true, d.downloadFile(ctx, svc, d.key, filepath.Join(tmpDir, filepath.Base(d.key)), s.MaxBytes)
}

func (d *Downloader) newService(ctx context.Context, s settings.Settings) (*storage.Service, error) {
	var opts []option.ClientOption

	endpoint := d.endpoint
	if endpoint == "" {
		endpoint = s.GCPCredentials.Endpoint
	}
	if endpoint != "" {
		opts = append(opts, option.WithEndpoint(endpoint))
	}

	if s.GCPCredentials.ServiceAccountKey != "" {
		// Validate it looks like JSON before using it.
		var js json.RawMessage
		if err := json.Unmarshal([]byte(s.GCPCredentials.ServiceAccountKey), &js); err != nil {
			return nil, fmt.Errorf("invalid service account key JSON: %w", err)
		}
		creds, err := google.CredentialsFromJSON(ctx, []byte(s.GCPCredentials.ServiceAccountKey), storage.DevstorageReadOnlyScope) //nolint:staticcheck // no replacement available yet
		if err != nil {
			return nil, fmt.Errorf("parsing service account key: %w", err)
		}
		opts = append(opts, option.WithTokenSource(creds.TokenSource))
	} else if endpoint != "" {
		// Custom endpoint (e.g. fake-gcs-server) — skip credential resolution.
		opts = append(opts, option.WithoutAuthentication())
	} else {
		// Try Application Default Credentials; fall back to anonymous access.
		ts, err := google.DefaultTokenSource(ctx, storage.DevstorageReadOnlyScope)
		if err != nil {
			// No credentials available — use anonymous access (works for public buckets).
			opts = append(opts, option.WithTokenSource(oauth2.StaticTokenSource(nil)))
		} else {
			opts = append(opts, option.WithTokenSource(ts))
		}
	}

	return storage.NewService(ctx, opts...)
}

func (d *Downloader) downloadFile(ctx context.Context, svc *storage.Service, key, dst string, maxBytes int64) error {
	resp, err := svc.Objects.Get(d.bucket, key).Context(ctx).Download()
	if err != nil {
		return fmt.Errorf("getting object gs://%s/%s: %w", d.bucket, key, err)
	}
	defer resp.Body.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := limitio.Copy(f, resp.Body, maxBytes); err != nil {
		return fmt.Errorf("downloading object gs://%s/%s: %w", d.bucket, key, err)
	}

	return nil
}

func (d *Downloader) downloadDir(ctx context.Context, svc *storage.Service, tmpDir string, maxBytes int64) error {
	prefix := d.key
	pageToken := ""

	for {
		call := svc.Objects.List(d.bucket).Prefix(prefix).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		objects, err := call.Do()
		if err != nil {
			return fmt.Errorf("listing objects in gs://%s/%s: %w", d.bucket, prefix, err)
		}

		for _, obj := range objects.Items {
			relPath := strings.TrimPrefix(obj.Name, prefix)
			if relPath == "" || strings.HasSuffix(relPath, "/") {
				continue
			}

			// The object name comes from the listing, which an attacker may
			// control; keep the write inside tmpDir.
			fileDst, err := safepath.Join(tmpDir, relPath)
			if err != nil {
				return fmt.Errorf("object gs://%s/%s: %w", d.bucket, obj.Name, err)
			}
			if err := d.downloadFile(ctx, svc, obj.Name, fileDst, maxBytes); err != nil {
				return err
			}
		}

		if objects.NextPageToken == "" {
			break
		}
		pageToken = objects.NextPageToken
	}

	return nil
}
