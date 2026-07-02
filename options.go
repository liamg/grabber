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

func WithOCICredentials(username, password string) Option {
	return func(g *Grabber) {
		g.settings.OCICredentials = settings.OCICredentials{
			Username: username,
			Password: password,
		}
	}
}

func WithOCIPlainHTTP() Option {
	return func(g *Grabber) {
		g.settings.OCICredentials.PlainHTTP = true
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

func WithGitSSHKey(key []byte) Option {
	return func(g *Grabber) {
		g.settings.Git.SSHKey = key
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
