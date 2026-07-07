package grabber

import (
	"net"
	"net/http"
	"net/url"

	"github.com/liamg/grabber/protocols"
	"github.com/liamg/grabber/settings"
	"github.com/liamg/grabber/ssrf"
)

// WithHTTPTransport sets the base transport used by the HTTP and OCI protocols
// (and, for its TLS/proxy settings, the Git archive fallback). Use it to
// control the outbound transport, e.g. to install an SSRF guard via a custom
// DialContext. grabber clones it per download and layers any configured CA
// pool, client certificate, and proxy onto the clone, so those options compose
// with the transport. It does not affect protocols that use their own clients
// (git clones map TLS/proxy onto go-git's per-clone options; s3/gcs use the SDK
// clients). A concrete *http.Transport is required so grabber can clone and
// extend it. If nil, a clone of http.DefaultTransport is used when any
// TLS/proxy customisation is configured.
func WithHTTPTransport(transport *http.Transport) Option {
	return func(g *Grabber) {
		g.settings.HTTPTransport = transport
	}
}

// WithTLSCACert adds a CA certificate (PEM) trusted for HTTPS connections,
// merged with the system roots. May be called multiple times to trust several
// CAs. Applies to the HTTP, OCI, and Git (HTTPS) protocols.
func WithTLSCACert(pem []byte) Option {
	return func(g *Grabber) {
		g.settings.TLSCACerts = append(g.settings.TLSCACerts, pem)
	}
}

// WithClientCertificate sets a default TLS client certificate (mutual TLS) used
// for any host without a more specific certificate.
func WithClientCertificate(certPEM, keyPEM []byte) Option {
	return func(g *Grabber) {
		g.settings.ClientCertificates = append(g.settings.ClientCertificates, settings.ClientCertificate{
			Cert: certPEM,
			Key:  keyPEM,
		})
	}
}

// WithClientCertificateForHost sets a TLS client certificate (mutual TLS) scoped
// to a specific host. It takes precedence over any default set via
// WithClientCertificate.
func WithClientCertificateForHost(host string, certPEM, keyPEM []byte) Option {
	return func(g *Grabber) {
		g.settings.ClientCertificates = append(g.settings.ClientCertificates, settings.ClientCertificate{
			Host: host,
			Cert: certPEM,
			Key:  keyPEM,
		})
	}
}

// WithHTTPProxy sets a global HTTP proxy for outbound HTTP/OCI/Git(HTTPS)
// requests. Pass empty user/pass for an unauthenticated proxy.
func WithHTTPProxy(u *url.URL, username, password string) Option {
	return func(g *Grabber) {
		g.settings.Proxies = append(g.settings.Proxies, settings.ProxyConfig{
			URL:      u,
			Username: username,
			Password: password,
		})
	}
}

// WithHTTPProxyForHost sets an HTTP proxy scoped to a specific host. It takes
// precedence over any global proxy set via WithHTTPProxy when it matches.
func WithHTTPProxyForHost(host string, u *url.URL, username, password string) Option {
	return func(g *Grabber) {
		g.settings.Proxies = append(g.settings.Proxies, settings.ProxyConfig{
			Host:     host,
			URL:      u,
			Username: username,
			Password: password,
		})
	}
}

// WithSSRFProtection sets the SSRF guard level applied to outbound connections
// (HTTP and OCI at dial time; a pre-fetch check for Git and Mercurial). The
// guard defaults to ssrf.Internal; pass ssrf.None to disable it. s3/gcs only
// reach fixed cloud endpoints and are not guarded.
func WithSSRFProtection(level ssrf.Level) Option {
	return func(g *Grabber) {
		g.settings.SSRFLevel = level
	}
}

// WithCustomSSRFProtection guards outbound connections with a caller-supplied
// predicate that reports whether a resolved IP must be blocked. It sets the
// level to ssrf.Custom.
func WithCustomSSRFProtection(blocked func(net.IP) bool) Option {
	return func(g *Grabber) {
		g.settings.SSRFLevel = ssrf.Custom
		g.settings.SSRFCustom = blocked
	}
}

// WithHTTPCredentialRequestFunction sets a callback that resolves HTTP
// basic-auth credentials dynamically for the HTTP, Git (HTTPS), and OCI
// protocols. It is consulted only when no static credential matches (and before
// the system git credential helper). The callback receives the protocol, host,
// and path, and returns the username and password (either may be nil) plus ok;
// returning ok=false defers to the next credential source. This is the
// in-memory replacement for an on-disk git credential helper.
func WithHTTPCredentialRequestFunction(f settings.CredentialRequestFunc) Option {
	return func(g *Grabber) {
		g.settings.HTTPCredentialRequest = f
	}
}

// WithSSRFAllowHosts adds hosts that bypass the SSRF guard entirely. Each entry
// may be a hostname (matched case-insensitively), an IP literal, or a CIDR
// range (matched against the resolved IP). Repeatable and additive.
func WithSSRFAllowHosts(hosts ...string) Option {
	return func(g *Grabber) {
		g.settings.SSRFAllow = append(g.settings.SSRFAllow, hosts...)
	}
}

func WithSparseCheckout(enabled bool) Option {
	return func(g *Grabber) {
		g.settings.Git.SparseCheckout = enabled
	}
}

func WithAutoExtract(enabled bool) Option {
	return func(g *Grabber) {
		g.settings.EnableAutoExtract = enabled
	}
}

func WithProtocols(protocols ...protocols.Protocol) Option {
	return func(g *Grabber) {
		g.protocols = protocols
	}
}

func WithAWSCredentials(accessKeyID, secretAccessKey, sessionToken, region string) Option {
	return func(g *Grabber) {
		g.settings.AWSCredentials = settings.AWSCredentials{
			AccessKeyID:     accessKeyID,
			SecretAccessKey: secretAccessKey,
			SessionToken:    sessionToken,
			Region:          region,
		}
	}
}

func WithGCPCredentials(serviceAccountKey string) Option {
	return func(g *Grabber) {
		g.settings.GCPCredentials = settings.GCPCredentials{
			ServiceAccountKey: serviceAccountKey,
		}
	}
}

// WithOCICredentials sets default credentials used for any OCI registry that
// has no more specific match configured via WithOCICredentialForRegistry.
func WithOCICredentials(username, password string) Option {
	return func(g *Grabber) {
		g.settings.OCICredentials = append(g.settings.OCICredentials, settings.OCICredential{
			Username: username,
			Password: password,
		})
	}
}

// WithOCICredentialForRegistry sets credentials scoped to a specific registry
// host (e.g. "ghcr.io"). These take precedence over any default credentials.
func WithOCICredentialForRegistry(registry, username, password string) Option {
	return func(g *Grabber) {
		g.settings.OCICredentials = append(g.settings.OCICredentials, settings.OCICredential{
			Registry: registry,
			Username: username,
			Password: password,
		})
	}
}

func WithOCIPlainHTTP() Option {
	return func(g *Grabber) {
		g.settings.OCIPlainHTTP = true
	}
}

func WithHTTPSCredential(host, username, password string) Option {
	return func(g *Grabber) {
		g.settings.HTTPSCredentials = append(g.settings.HTTPSCredentials, settings.HTTPSCredential{
			Host:     host,
			Username: username,
			Password: password,
		})
	}
}

func WithHTTPSCredentialForPath(host, path, username, password string) Option {
	return func(g *Grabber) {
		g.settings.HTTPSCredentials = append(g.settings.HTTPSCredentials, settings.HTTPSCredential{
			Host:     host,
			Path:     path,
			Username: username,
			Password: password,
		})
	}
}

func WithGitSSHToHTTPS() Option {
	return func(g *Grabber) {
		g.settings.Git.SSHToHTTPS = true
	}
}

// WithGitSSHKey sets a default SSH private key used for any host that has no
// more specific key configured via WithGitSSHKeyForHost.
func WithGitSSHKey(key []byte) Option {
	return func(g *Grabber) {
		g.settings.Git.SSHKeys = append(g.settings.Git.SSHKeys, settings.SSHCredential{
			Key: key,
		})
	}
}

// WithGitSSHKeyForHost sets an SSH private key scoped to a specific host (e.g.
// "github.com"). It takes precedence over any default key set via WithGitSSHKey.
func WithGitSSHKeyForHost(host string, key []byte) Option {
	return func(g *Grabber) {
		g.settings.Git.SSHKeys = append(g.settings.Git.SSHKeys, settings.SSHCredential{
			Host: host,
			Key:  key,
		})
	}
}

func WithGitDepth(depth int) Option {
	return func(g *Grabber) {
		g.settings.Git.Depth = depth
	}
}

func WithGitInsecureSkipHostKeyVerify() Option {
	return func(g *Grabber) {
		g.settings.Git.InsecureSkipHostKeyVerify = true
	}
}

// WithGitKnownHosts verifies SSH host keys against the given known_hosts data
// in memory (nothing is read from disk). Unknown hosts are allowed, but a host
// whose key has changed from the one recorded here is rejected. Hashed
// known_hosts entries (|1|...) are not supported and are ignored. This takes
// precedence over the system ~/.ssh/known_hosts default.
func WithGitKnownHosts(knownHosts []byte) Option {
	return func(g *Grabber) {
		g.settings.Git.KnownHosts = knownHosts
	}
}

// WithNoSystemFallback disables all ambient/system credential and execution
// fallbacks, so grabber relies only on what is provided via functional options.
// See settings.Settings.NoSystemFallback for the full list of what it disables.
func WithNoSystemFallback() Option {
	return func(g *Grabber) {
		g.settings.NoSystemFallback = true
	}
}
