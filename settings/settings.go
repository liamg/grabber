package settings

import (
	"net/url"
	"os"
	"strings"
)

var Defaults = Settings{
	TemporaryDirectory: os.TempDir(),
	EnableAutoExtract:  true,
}

type Settings struct {
	// Only fetch the subdirectory specified via // syntax, rather than cloning the entire repo.
	// Only supported by Git.
	EnableSparseCheckout bool

	// Automatically detect and extract archives after download (e.g. .tar.gz, .zip).
	EnableAutoExtract bool

	// Credentials for accessing S3 buckets. Falls back to the default AWS SDK credential chain.
	AWSCredentials AWSCredentials

	// Credentials for accessing GCS buckets. Falls back to the default GCP SDK credential chain.
	GCPCredentials GCPCredentials

	// Credentials for accessing OCI registries.
	OCICredentials OCICredentials

	// HTTPS credentials matched by host (and optionally path prefix) for Git and HTTP downloads.
	// Matching follows git credential helper semantics — see MatchHTTPSCredential.
	HTTPSCredentials []HTTPSCredential

	// Git-specific settings (SSH keys, clone depth, SSH-to-HTTPS conversion, etc.).
	Git GitConfig

	// Working directory for intermediate files during download/extraction.
	// Defaults to os.TempDir().
	TemporaryDirectory string
}

type AWSCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Region          string
}

type GCPCredentials struct {
	ServiceAccountKey string
	Endpoint          string // custom endpoint URL (for testing or GCS-compatible services)
}

type OCICredentials struct {
	Username  string
	Password  string
	PlainHTTP bool // use HTTP instead of HTTPS (for local registries)
}

type GitConfig struct {
	SSHKey                    []byte // private key bytes
	Depth                     int    // 0 = full clone
	InsecureSkipHostKeyVerify bool   // skip SSH host key verification
	SSHToHTTPS                bool   // convert SSH/SCP Git URLs to HTTPS before cloning
}

// HTTPSCredential represents a credential for HTTPS URLs.
// Matching follows git credential helper semantics: scheme + host must match,
// and if a path is specified it must be a prefix of the request URL path.
type HTTPSCredential struct {
	Host     string // required, e.g. "github.com"
	Username string
	Password string
	Path     string // optional path prefix, e.g. "/org/repo"
}

// MatchHTTPSCredential finds the best matching HTTPS credential for the given
// URL. Matching works like git credential helpers: host must match, and if the
// credential has a path, it must be a prefix of the URL path. The most specific
// match (longest path prefix) wins. Returns nil if no credential matches.
func (s Settings) MatchHTTPSCredential(rawURL string) *HTTPSCredential {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}

	var best *HTTPSCredential
	bestPathLen := -1

	for i := range s.HTTPSCredentials {
		cred := &s.HTTPSCredentials[i]

		if !strings.EqualFold(cred.Host, u.Hostname()) {
			continue
		}

		if cred.Path != "" {
			credPath := strings.TrimSuffix(cred.Path, "/")
			urlPath := strings.TrimSuffix(u.Path, "/")
			if !strings.HasPrefix(urlPath, credPath) {
				continue
			}
			if len(credPath) > bestPathLen {
				bestPathLen = len(credPath)
				best = cred
			}
		} else if bestPathLen < 0 {
			// Host-only match — use if no path-specific match found yet.
			best = cred
		}
	}

	return best
}
