package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/liamg/grabber/internal/limitio"
	"github.com/liamg/grabber/internal/netrc"
	"github.com/liamg/grabber/protocols"
	"github.com/liamg/grabber/settings"
)

type Protocol struct{}

var _ protocols.Protocol = (*Protocol)(nil)

func New() *Protocol {
	return &Protocol{}
}

func (p *Protocol) Prefix() string {
	return "http"
}

func (p *Protocol) Priority() int {
	return 20
}

func (p *Protocol) Detect(rawURL string) (protocols.Downloadable, bool) {
	d, err := parseHTTPURL(rawURL)
	if err != nil {
		return nil, false
	}
	return d, true
}

func parseHTTPURL(rawURL string) (*Downloader, error) {
	// Reject file paths — these should be handled by the file protocol.
	if strings.HasPrefix(rawURL, "/") || strings.HasPrefix(rawURL, "./") || strings.HasPrefix(rawURL, "../") {
		return nil, errors.New("not an HTTP URL")
	}

	// Add scheme if missing.
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("not an HTTP URL")
	}

	if u.Host == "" {
		return nil, errors.New("no host specified")
	}

	// "?archive=<format>" names the archive format explicitly. It is a directive
	// to the getter rather than something the server understands, so it is
	// stripped from the request URL.
	archive := ""
	if q := u.Query(); q.Has("archive") {
		archive = q.Get("archive")
		q.Del("archive")
		u.RawQuery = q.Encode()
	}

	return &Downloader{
		url:     u.String(),
		archive: archive,
	}, nil
}

type Downloader struct {
	url string
	// archive is the value of the "?archive=" parameter, empty when absent. See
	// fileName for how it is applied.
	archive string
}

// maxFileName is the longest single path component POSIX filesystems accept.
const maxFileName = 255

// fileName returns the name to write the response body to.
//
// Extraction happens after the download and selects an extractor from the
// file's extension, so the name is what decides whether an archive is unpacked.
//
// The URL path is the usual source, but it cannot always supply one: a Terraform
// Cloud registry download is served from an "archivist" URL whose last path
// segment is a ~400 character opaque token with no extension — too long to
// create, and no use to the extractor even if it were. Such URLs carry the
// format in "?archive=" instead, which is what this prefers.
func (d *Downloader) fileName() string {
	if d.archive != "" {
		// A boolean disables extraction rather than naming a format, matching
		// the convention the parameter comes from.
		if _, err := strconv.ParseBool(d.archive); err == nil {
			return "download"
		}
		return "archive." + d.archive
	}

	u, err := url.Parse(d.url)
	if err != nil {
		return "download"
	}
	name := path.Base(u.Path)
	if name == "" || name == "." || name == "/" {
		return "download"
	}
	if len(name) > maxFileName {
		// Keep the tail: any extension lives there, and the extractor needs it.
		return name[len(name)-maxFileName:]
	}
	return name
}

var _ protocols.Downloadable = (*Downloader)(nil)

// httpPort returns the port a request to u connects to, defaulting to 443 for
// https and 80 otherwise.
func httpPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

func (d *Downloader) Download(ctx context.Context, tmpDir string, s settings.Settings) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return false, fmt.Errorf("creating request: %w", err)
	}

	// Resolve credentials: embedded URL userinfo wins; otherwise a configured
	// static credential; otherwise the dynamic request function; otherwise
	// netrc (when enabled).
	if req.URL.User == nil {
		if cred := s.MatchHTTPSCredential(d.url); cred != nil {
			req.SetBasicAuth(cred.Username, cred.Password)
		} else if user, pass, ok := s.RequestCredential(ctx, req.URL.Scheme, req.URL.Hostname(), req.URL.Path); ok {
			req.SetBasicAuth(user, pass)
		} else if s.Netrc {
			if m, err := netrc.Lookup(req.URL.Hostname()); err == nil && m != nil && m.Login != "" {
				req.SetBasicAuth(m.Login, m.Password)
			}
		}
	}

	// Fail fast if the host is unreachable, rather than hanging until the
	// context deadline.
	if err := s.ProbeConnect(ctx, req.URL.Hostname(), httpPort(req.URL)); err != nil {
		return false, err
	}

	client := http.DefaultClient
	tr, err := s.TransportForHost(req.URL.Hostname())
	if err != nil {
		return false, fmt.Errorf("configuring HTTP transport: %w", err)
	}
	if tr != nil {
		client = &http.Client{Transport: tr}
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("downloading %s: %w", d.url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("downloading %s: HTTP %d", d.url, resp.StatusCode)
	}

	dst := filepath.Join(tmpDir, d.fileName())

	f, err := os.Create(dst)
	if err != nil {
		return false, fmt.Errorf("creating file: %w", err)
	}
	defer f.Close()

	if _, err := limitio.Copy(f, resp.Body, s.MaxBytes); err != nil {
		return false, fmt.Errorf("downloading %s: %w", d.url, err)
	}

	return true, nil
}
