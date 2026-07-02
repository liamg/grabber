package settings

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
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

	// NoSystemFallback disables all ambient/system credential and execution
	// fallbacks, so grabber uses only what is provided via functional options.
	// When true it disables: the SSH agent, the system `git credential` helper,
	// the archive-fallback environment-variable token lookup (GH_TOKEN etc.),
	// the ~/.ssh/known_hosts default host-key check, and the Mercurial (hg)
	// subprocess (hg downloads error out). Intended for locked-down environments
	// that must not read the ambient user/system configuration.
	NoSystemFallback bool

	// Working directory for intermediate files during download/extraction.
	// Defaults to os.TempDir().
	TemporaryDirectory string

	// HTTPTransport is the base transport used by the HTTP and OCI protocols
	// (and, for its TLS/proxy settings, the Git archive fallback). It lets
	// callers control the outbound transport — for example to install an SSRF
	// guard via a custom DialContext. grabber clones it per download and layers
	// the configured CA pool, client certificate, and proxy onto the clone, so
	// those settings compose with the caller's transport. Because net/http
	// re-dials through the transport on every request and redirect, a guard
	// installed here also covers DNS-rebinding and redirect-to-internal attacks.
	//
	// Git clones over HTTPS honour the CA/client-cert/proxy settings via go-git's
	// per-clone options rather than this transport. It does not affect s3/gcs
	// (which use their own SDK clients). If nil, a clone of http.DefaultTransport
	// is used when any TLS/proxy customisation is configured.
	HTTPTransport *http.Transport

	// TLSCACerts are additional CA certificates (PEM) trusted for HTTPS
	// connections, merged with the system roots into a single pool. Applies to
	// the HTTP, OCI, and Git (HTTPS) protocols.
	TLSCACerts [][]byte

	// ClientCertificates are TLS client certificates for mutual TLS, matched by
	// host (see MatchClientCertificate).
	ClientCertificates []ClientCertificate

	// Proxies configure an HTTP proxy for outbound HTTP/OCI/Git(HTTPS) requests,
	// matched by host (see MatchProxy).
	Proxies []ProxyConfig
}

// ClientCertificate is a TLS client certificate (PEM-encoded cert and key) for
// mutual TLS. Matching is by host; a certificate with an empty Host acts as the
// default for any host with no more specific match.
type ClientCertificate struct {
	Host string // e.g. "github.example.com"; empty matches any host
	Cert []byte // PEM-encoded certificate
	Key  []byte // PEM-encoded private key
}

// ProxyConfig configures an HTTP proxy. Matching is by host; a config with an
// empty Host acts as the global proxy for any host with no more specific match.
type ProxyConfig struct {
	Host     string // e.g. "registry.terraform.io"; empty is the global proxy
	URL      *url.URL
	Username string
	Password string
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
	KnownHosts                []byte          // known_hosts data for in-memory SSH host-key verification
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

// MatchClientCertificate finds the best matching TLS client certificate for the
// given host. A certificate whose Host matches (case-insensitive) wins;
// otherwise one with an empty Host is used as the default. Returns nil if none
// match.
func (s Settings) MatchClientCertificate(host string) *ClientCertificate {
	var fallback *ClientCertificate
	for i := range s.ClientCertificates {
		c := &s.ClientCertificates[i]
		if c.Host == "" {
			if fallback == nil {
				fallback = c
			}
			continue
		}
		if strings.EqualFold(c.Host, host) {
			return c
		}
	}
	return fallback
}

// MatchProxy finds the best matching proxy for the given host. A proxy whose
// Host matches (case-insensitive) wins; otherwise one with an empty Host (the
// global proxy) is used. Returns nil if none match.
func (s Settings) MatchProxy(host string) *ProxyConfig {
	var fallback *ProxyConfig
	for i := range s.Proxies {
		p := &s.Proxies[i]
		if p.Host == "" {
			if fallback == nil {
				fallback = p
			}
			continue
		}
		if strings.EqualFold(p.Host, host) {
			return p
		}
	}
	return fallback
}

// caCertPool returns a certificate pool of the system roots plus any configured
// TLSCACerts, or (nil, nil) when none are configured so callers use the system
// default.
func (s Settings) caCertPool() (*x509.CertPool, error) {
	if len(s.TLSCACerts) == 0 {
		return nil, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("loading system cert pool: %w", err)
	}
	if pool == nil {
		pool = x509.NewCertPool()
	}
	for _, pem := range s.TLSCACerts {
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
	}
	return pool, nil
}

// ProxyURL returns the proxy URL with any configured credentials embedded, or
// nil if no URL is set.
func (p *ProxyConfig) ProxyURL() *url.URL {
	if p == nil || p.URL == nil {
		return nil
	}
	u := *p.URL
	if p.Username != "" || p.Password != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return &u
}

// TransportForHost builds the *http.Transport to use for requests to host,
// layering the configured CA pool, matched client certificate, and matched
// proxy onto the base transport (HTTPTransport, or a clone of
// http.DefaultTransport when customisation is needed). It returns (nil, nil)
// when nothing is configured, so callers fall back to their default client.
func (s Settings) TransportForHost(host string) (*http.Transport, error) {
	pool, err := s.caCertPool()
	if err != nil {
		return nil, err
	}
	cert := s.MatchClientCertificate(host)
	proxy := s.MatchProxy(host)

	if s.HTTPTransport == nil && pool == nil && cert == nil && proxy == nil {
		return nil, nil
	}

	var base *http.Transport
	if s.HTTPTransport != nil {
		base = s.HTTPTransport.Clone()
	} else if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		base = dt.Clone()
	} else {
		base = &http.Transport{}
	}

	if pool != nil || cert != nil {
		if base.TLSClientConfig == nil {
			base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			base.TLSClientConfig = base.TLSClientConfig.Clone()
		}
		if pool != nil {
			base.TLSClientConfig.RootCAs = pool
		}
		if cert != nil {
			keyPair, err := tls.X509KeyPair(cert.Cert, cert.Key)
			if err != nil {
				return nil, fmt.Errorf("loading client certificate: %w", err)
			}
			base.TLSClientConfig.Certificates = []tls.Certificate{keyPair}
		}
	}

	if proxy != nil {
		base.Proxy = http.ProxyURL(proxy.ProxyURL())
	}

	return base, nil
}
