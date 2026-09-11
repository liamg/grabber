package git

import (
	"context"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liamg/grabber/settings"
	"github.com/liamg/grabber/ssrf"
)

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*nethttp.Request) (*nethttp.Response, error)

func (f roundTripFunc) RoundTrip(r *nethttp.Request) (*nethttp.Response, error) { return f(r) }

// respond builds a response the stripper can consume.
func respond(status int, contentType, body string) *nethttp.Response {
	h := nethttp.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return &nethttp.Response{
		StatusCode:    status,
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func TestErrBodyStripper(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		status      int
		want        string
	}{
		{
			name:   "short plain body on an error is kept",
			status: nethttp.StatusNotFound,
			body:   "Repository not found.",
			want:   "Repository not found.",
		},
		{
			name:        "html error page is dropped",
			status:      nethttp.StatusBadGateway,
			contentType: "text/html; charset=utf-8",
			body:        "<html><body>502 at 2026-09-11T08:00:00Z</body></html>",
			want:        "",
		},
		{
			name:   "html served as text/plain is sniffed and dropped",
			status: nethttp.StatusBadGateway,
			// No content type at all: the body itself must give it away.
			body: "\n  <!DOCTYPE html><html><body>nope</body></html>",
			want: "",
		},
		{
			name:   "oversized plain body is dropped, not truncated",
			status: nethttp.StatusInternalServerError,
			body:   strings.Repeat("x", maxErrBody+1),
			want:   "",
		},
		{
			name:   "body at the limit is kept",
			status: nethttp.StatusInternalServerError,
			body:   strings.Repeat("x", maxErrBody),
			want:   strings.Repeat("x", maxErrBody),
		},
		{
			name:        "successful responses are untouched",
			status:      nethttp.StatusOK,
			contentType: "text/html",
			body:        strings.Repeat("y", maxErrBody+1),
			want:        strings.Repeat("y", maxErrBody+1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := errBodyStripper{base: roundTripFunc(func(*nethttp.Request) (*nethttp.Response, error) {
				return respond(tt.status, tt.contentType, tt.body), nil
			})}

			resp, err := rt.RoundTrip(&nethttp.Request{})
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("reading body: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("body = %q (%d bytes), want %q (%d bytes)",
					truncate(string(got)), len(got), truncate(tt.want), len(tt.want))
			}
			if resp.ContentLength != int64(len(got)) {
				t.Errorf("ContentLength = %d, want %d", resp.ContentLength, len(got))
			}
		})
	}
}

// TestDownload_HTMLErrorPageStaysOutOfTheError proves the end-to-end effect:
// go-git copies a failed response body into its error string, so a large HTML
// error page must never reach it.
func TestDownload_HTMLErrorPageStaysOutOfTheError(t *testing.T) {
	page := "<html><body>" + strings.Repeat("a", 200*1024) + "</body></html>"

	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(nethttp.StatusBadGateway)
		_, _ = io.WriteString(w, page)
	}))
	t.Cleanup(srv.Close)

	d := &Downloader{repoURL: srv.URL + "/repo.git"}
	_, err := d.Download(context.Background(), t.TempDir(),
		settings.Settings{SSRFLevel: ssrf.None, NoSystemFallback: true})
	if err == nil {
		t.Fatal("expected the 502 to fail the download")
	}
	if strings.Contains(err.Error(), "<html") || strings.Contains(err.Error(), "aaaa") {
		t.Errorf("error carries the HTML page: %s", truncate(err.Error()))
	}
	if len(err.Error()) > 4096 {
		t.Errorf("error is %d bytes, want a short one: %s", len(err.Error()), truncate(err.Error()))
	}
}

func truncate(s string) string {
	if len(s) <= 200 {
		return s
	}
	return s[:200] + "…"
}
