package settings

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

var Defaults = Settings{
	TemporaryDirectory: os.TempDir(),
	EnableAutoExtract:  true,
}

type Settings struct {
	// Automatically detect and extract archives after download (e.g. .tar.gz, .zip).
	EnableAutoExtract bool

	// Credentials for accessing S3 buckets. Falls back to the default AWS SDK credential chain.
	AWSCredentials AWSCredentials

	// Credentials for accessing GCS buckets. Falls back to the default GCP SDK credential chain.
	GCPCredentials GCPCredentials

	// Credentials for accessing OCI registries, matched by registry host.
	// Matching follows the same semantics as MatchOCICredential.
	OCICredentials []OCICredential

	// Use HTTP instead of HTTPS when talking to OCI registries (for local registries).
	OCIPlainHTTP bool

	// HTTPS credentials matched by host (and optionally path prefix) for Git and HTTP downloads.
	// Matching follows git credential helper semantics — see MatchHTTPSCredential.
	HTTPSCredentials []HTTPSCredential

	// Git-specific settings (SSH keys, clone depth, SSH-to-HTTPS conversion, etc.).
	Git GitConfig

	// Working directory for intermediate files during download/extraction.
	// Defaults to os.TempDir().
	TemporaryDirectory string

	// HTTPTransport is the round tripper used by the HTTP and OCI protocols. It
	// lets callers control the outbound transport — for example to install an
	// SSRF guard or a custom proxy. Because net/http re-dials through the
	// transport on every request and redirect, a guard installed here also
	// covers DNS-rebinding and redirect-to-internal attacks. The OCI protocol
	// layers its retry policy on top of this transport, so retries are preserved.
	//
	// It does not affect protocols that use their own clients (git, hg, s3, gcs).
	// If nil, each protocol falls back to its default transport
	// (http.DefaultTransport).
	HTTPTransport http.RoundTripper
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

// OCICredential represents a credential for an OCI registry.
// Matching is by registry host; a credential with an empty Registry
// acts as the default for any registry with no more specific match.
type OCICredential struct {
	Registry string // e.g. "ghcr.io"; empty matches any registry
	Username string
	Password string
}

type GitConfig struct {
	SSHKeys                   []SSHCredential // SSH private keys matched by host
	Depth                     int             // 0 = full clone
	SparseCheckout            bool            // only fetch the subdirectory specified via // syntax
	InsecureSkipHostKeyVerify bool            // skip SSH host key verification
	SSHToHTTPS                bool            // convert SSH/SCP Git URLs to HTTPS before cloning
}

// SSHCredential represents an SSH private key for Git over SSH.
// Matching is by host; a credential with an empty Host acts as the
// default for any host with no more specific match.
type SSHCredential struct {
	Host string // e.g. "github.com"; empty matches any host
	Key  []byte // private key bytes
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

// MatchSSHKey finds the best matching SSH private key for the given host. A
// credential whose Host matches (case-insensitive) wins; otherwise a credential
// with an empty Host is used as the default. Returns nil if none match.
func (s Settings) MatchSSHKey(host string) []byte {
	var fallback []byte
	for i := range s.Git.SSHKeys {
		cred := &s.Git.SSHKeys[i]
		if cred.Host == "" {
			if fallback == nil {
				fallback = cred.Key
			}
			continue
		}
		if strings.EqualFold(cred.Host, host) {
			return cred.Key
		}
	}
	return fallback
}

// MatchOCICredential finds the best matching credential for the given registry
// host. A credential whose Registry matches (case-insensitive) wins; otherwise a
// credential with an empty Registry is used as the default. Returns nil if none
// match.
func (s Settings) MatchOCICredential(registry string) *OCICredential {
	var fallback *OCICredential
	for i := range s.OCICredentials {
		cred := &s.OCICredentials[i]
		if cred.Registry == "" {
			if fallback == nil {
				fallback = cred
			}
			continue
		}
		if strings.EqualFold(cred.Registry, registry) {
			return cred
		}
	}
	return fallback
}
