package grabber

import (
	"net/http"

	"github.com/liamg/grabber/protocols"
	"github.com/liamg/grabber/settings"
)

// WithHTTPTransport sets the round tripper used by the HTTP and OCI protocols.
// Use it to control the outbound transport, e.g. to install an SSRF guard or a
// custom proxy. The OCI protocol layers its retry policy on top of the
// transport, so retries are preserved. It does not affect protocols that use
// their own clients (git, hg, s3, gcs). If nil, each protocol uses its default
// transport.
func WithHTTPTransport(rt http.RoundTripper) Option {
	return func(g *Grabber) {
		g.settings.HTTPTransport = rt
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
