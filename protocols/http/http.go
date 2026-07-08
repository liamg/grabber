package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

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

	return &Downloader{
		url: u.String(),
	}, nil
}

type Downloader struct {
	url string
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
	// static credential; otherwise the dynamic request function.
	if req.URL.User == nil {
		if cred := s.MatchHTTPSCredential(d.url); cred != nil {
			req.SetBasicAuth(cred.Username, cred.Password)
		} else if user, pass, ok := s.RequestCredential(ctx, req.URL.Scheme, req.URL.Hostname(), req.URL.Path); ok {
			req.SetBasicAuth(user, pass)
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

	// Determine the filename from the URL path.
	u, _ := url.Parse(d.url)
	filename := path.Base(u.Path)
	if filename == "" || filename == "." || filename == "/" {
		filename = "download"
	}

	dst := filepath.Join(tmpDir, filename)

	f, err := os.Create(dst)
	if err != nil {
		return false, fmt.Errorf("creating file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return false, fmt.Errorf("writing file: %w", err)
	}

	return true, nil
}
