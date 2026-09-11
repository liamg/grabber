package git

import (
	"bytes"
	"io"
	nethttp "net/http"
	"strings"
)

// maxErrBody bounds how much of a failed response body is kept. go-git copies
// the whole body into its transport error string, so an HTML error page from a
// proxy or registry would otherwise become a multi-hundred-KB error.
const maxErrBody = 1024

// errBodyStripper discards non-2xx response bodies that are HTML or larger than
// maxErrBody. Oversized bodies are dropped rather than truncated: a truncated
// body still carries timestamps and request IDs, which makes every error string
// unique and defeats downstream deduplication.
type errBodyStripper struct {
	base nethttp.RoundTripper
}

func (s errBodyStripper) RoundTrip(req *nethttp.Request) (*nethttp.Response, error) {
	resp, err := s.base.RoundTrip(req)
	if err != nil || resp.StatusCode < 300 || resp.Body == nil {
		return resp, err
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrBody+1))
	_ = resp.Body.Close()
	if readErr != nil || len(body) > maxErrBody || isHTMLBody(resp.Header.Get("Content-Type"), body) {
		body = nil
	}

	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp, nil
}

// isHTMLBody reports whether a response body is an HTML page. Some proxies
// serve HTML as text/plain, so the body itself is sniffed too.
func isHTMLBody(contentType string, body []byte) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/html") {
		return true
	}
	start := bytes.ToLower(bytes.TrimLeft(body, " \t\r\n"))
	return bytes.HasPrefix(start, []byte("<!doctype")) || bytes.HasPrefix(start, []byte("<html"))
}
