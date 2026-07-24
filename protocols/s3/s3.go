package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/liamg/grabber/protocols"
	"github.com/liamg/grabber/settings"
)

type Protocol struct{}

var _ protocols.Protocol = (*Protocol)(nil)

func New() *Protocol {
	return &Protocol{}
}

func (p *Protocol) Prefix() string {
	return "s3"
}

func (p *Protocol) Priority() int {
	return 70
}

func (p *Protocol) Detect(rawURL string) (protocols.Downloadable, bool) {
	d, err := parseS3URL(rawURL, false)
	if err != nil {
		return nil, false
	}
	return d, true
}

var _ protocols.ForcedDetector = (*Protocol)(nil)

// DetectForced handles URLs that carried the "s3::" force-prefix. Because the
// caller has explicitly committed to S3, it additionally accepts custom
// S3-compatible endpoints (MinIO, DigitalOcean Spaces, ...) whose host is not an
// amazonaws.com domain. Auto-detection (Detect) stays strict so a bare
// https:// URL is never mistaken for S3.
func (p *Protocol) DetectForced(rawURL string) (protocols.Downloadable, bool) {
	d, err := parseS3URL(rawURL, true)
	if err != nil {
		return nil, false
	}
	return d, true
}

// parseS3URL parses an S3 URL and returns a Downloader. When allowCustomEndpoint
// is true, a non-amazonaws.com host is treated as a custom S3-compatible
// endpoint (path-style, bucket = first path segment) rather than rejected; this
// is only enabled for URLs that used the "s3::" force-prefix.
//
// Supported formats:
//   - s3://bucket/key
//   - https://s3.amazonaws.com/bucket/key
//   - https://s3.us-west-2.amazonaws.com/bucket/key
//   - https://bucket.s3.amazonaws.com/key
//   - https://bucket.s3.us-west-2.amazonaws.com/key
//   - https://custom-host/bucket/key            (custom endpoint, forced only)
func parseS3URL(rawURL string, allowCustomEndpoint bool) (*Downloader, error) {
	// Handle s3://bucket/key format (AWS CLI style).
	if strings.HasPrefix(rawURL, "s3://") {
		path := strings.TrimPrefix(rawURL, "s3://")
		path = strings.TrimPrefix(path, "/")
		if path == "" {
			return nil, errors.New("no bucket specified")
		}
		slashIdx := strings.Index(path, "/")
		if slashIdx == -1 {
			return &Downloader{bucket: path}, nil
		}
		return &Downloader{
			bucket: path[:slashIdx],
			key:    path[slashIdx+1:],
		}, nil
	}

	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	host := strings.ToLower(u.Hostname())
	if !strings.Contains(host, "amazonaws.com") {
		if !allowCustomEndpoint {
			return nil, errors.New("not an S3 URL")
		}
		return parseCustomEndpointURL(u)
	}

	parts := strings.Split(host, ".")
	// Find the "s3" segment to determine the format.
	s3Idx := -1
	for i, part := range parts {
		if part == "s3" {
			s3Idx = i
			break
		}
	}
	if s3Idx == -1 {
		return nil, errors.New("not an S3 URL")
	}

	var bucket, key, region string

	if s3Idx == 0 {
		// Path-style: s3.amazonaws.com/bucket/key or s3.us-west-2.amazonaws.com/bucket/key
		path := strings.TrimPrefix(u.Path, "/")
		slashIdx := strings.Index(path, "/")
		if slashIdx == -1 {
			bucket = path
		} else {
			bucket = path[:slashIdx]
			key = path[slashIdx+1:]
		}
		// Check for regional endpoint: s3.REGION.amazonaws.com
		if s3Idx+1 < len(parts) && parts[s3Idx+1] != "amazonaws" {
			region = parts[s3Idx+1]
		}
	} else {
		// Virtual-hosted style: bucket.s3.amazonaws.com/key or bucket.s3.us-west-2.amazonaws.com/key
		bucket = strings.Join(parts[:s3Idx], ".")
		key = strings.TrimPrefix(u.Path, "/")
		// Check for regional endpoint
		if s3Idx+1 < len(parts) && parts[s3Idx+1] != "amazonaws" {
			region = parts[s3Idx+1]
		}
	}

	if bucket == "" {
		return nil, errors.New("no bucket specified")
	}

	return &Downloader{
		bucket: bucket,
		key:    key,
		region: region,
	}, nil
}

// parseCustomEndpointURL parses a custom S3-compatible endpoint URL (MinIO,
// DigitalOcean Spaces, ...) using path-style addressing: the first path segment
// is the bucket and the remainder is the key. The endpoint is the scheme+host of
// the URL, and the region comes from the ?region= query (defaulting to
// us-east-1, which S3-compatible servers generally ignore).
func parseCustomEndpointURL(u *url.URL) (*Downloader, error) {
	path := strings.TrimPrefix(u.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	if bucket == "" {
		return nil, errors.New("no bucket specified")
	}

	region := u.Query().Get("region")
	if region == "" {
		region = "us-east-1"
	}

	return &Downloader{
		bucket:   bucket,
		key:      key,
		region:   region,
		endpoint: u.Scheme + "://" + u.Host,
	}, nil
}

type Downloader struct {
	bucket string
	key    string
	region string
	// endpoint is a custom S3-compatible endpoint (scheme://host). Empty for
	// real AWS S3, in which case the SDK resolves the endpoint from the region.
	endpoint string
}

var _ protocols.Downloadable = (*Downloader)(nil)

func (d *Downloader) Download(ctx context.Context, tmpDir string, s settings.Settings) (bool, error) {
	client, err := d.newClient(ctx, s)
	if err != nil {
		return false, fmt.Errorf("creating S3 client: %w", err)
	}

	// If the key ends with "/" or is empty, treat as a directory listing.
	if d.key == "" || strings.HasSuffix(d.key, "/") {
		return false, d.downloadDir(ctx, client, tmpDir)
	}
	return true, d.downloadFile(ctx, client, d.key, filepath.Join(tmpDir, filepath.Base(d.key)))
}

func (d *Downloader) resolveRegion(s settings.Settings) string {
	if s.AWSCredentials.Region != "" {
		return s.AWSCredentials.Region
	}
	if d.region != "" {
		return d.region
	}
	return "us-east-1"
}

func (d *Downloader) newClient(ctx context.Context, s settings.Settings) (*s3.Client, error) {
	region := d.resolveRegion(s)

	var opts []func(*awsconfig.LoadOptions) error
	opts = append(opts, awsconfig.WithRegion(region))

	if s.AWSCredentials.AccessKeyID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				s.AWSCredentials.AccessKeyID,
				s.AWSCredentials.SecretAccessKey,
				s.AWSCredentials.SessionToken,
			),
		))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}

	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		// Custom S3-compatible endpoints (MinIO, DO Spaces, ...) require
		// path-style addressing and an explicit base endpoint; virtual-hosted
		// addressing assumes the bucket is a subdomain of an AWS host.
		if d.endpoint != "" {
			o.BaseEndpoint = aws.String(d.endpoint)
			o.UsePathStyle = true
		}
	}), nil
}

func (d *Downloader) downloadFile(ctx context.Context, client *s3.Client, key, dst string) error {
	resp, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("getting object s3://%s/%s: %w", d.bucket, key, err)
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

	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}

	return nil
}

func (d *Downloader) downloadDir(ctx context.Context, client *s3.Client, tmpDir string) error {
	prefix := d.key
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: aws.String(d.bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("listing objects in s3://%s/%s: %w", d.bucket, prefix, err)
		}

		for _, obj := range page.Contents {
			if obj.Key == nil {
				continue
			}

			// Strip the prefix to get the relative path within dst.
			relPath := strings.TrimPrefix(*obj.Key, prefix)
			if relPath == "" || strings.HasSuffix(relPath, "/") {
				// Skip "directory" markers.
				continue
			}

			fileDst := filepath.Join(tmpDir, relPath)
			if err := d.downloadFile(ctx, client, *obj.Key, fileDst); err != nil {
				return err
			}
		}
	}

	return nil
}
