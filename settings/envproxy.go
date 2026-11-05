package settings

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/liamg/grabber/ssrf"
)

// proxyDecisionKey is the context key under which a *proxyDecision lives for one
// RoundTrip. The transport's Proxy function records there whether the request
// was routed through a proxy, so the dialer can exempt that one connection from
// the SSRF guard (it goes to the proxy, not to the target).
type proxyDecisionKey struct{}

type proxyDecision struct {
	proxied atomic.Bool
}

func dialIsProxied(ctx context.Context) bool {
	d, ok := ctx.Value(proxyDecisionKey{}).(*proxyDecision)
	return ok && d.proxied.Load()
}

// envProxyTransport lets a guarded transport use a proxy it inherited from the
// environment (HTTPS_PROXY / HTTP_PROXY, honouring NO_PROXY), including one on a
// private address.
//
// The exemption is per request, not per address: the dial is exempt only when
// the Proxy function chose a proxy for that request. A source pointed straight
// at the proxy's address is still guarded when NO_PROXY makes it a direct dial.
// And because the proxy makes the connection to the target, the target is
// checked here, before the request is handed over (see Guard.CheckProxiedHost).
type envProxyTransport struct {
	inner *http.Transport
}

// newEnvProxyTransport wraps tr, which is modified in place: its Proxy and
// DialContext are replaced.
func newEnvProxyTransport(tr *http.Transport, guard *ssrf.Guard) http.RoundTripper {
	orig := tr.Proxy
	tr.Proxy = func(req *http.Request) (*url.URL, error) {
		d, _ := req.Context().Value(proxyDecisionKey{}).(*proxyDecision)
		u, err := orig(req)
		if err != nil || u == nil {
			if d != nil {
				d.proxied.Store(false)
			}
			return u, err
		}
		// The proxy is told the target by the request line (plain HTTP) or the
		// CONNECT line (HTTPS). For plain HTTP net/http writes req.Host, when
		// set, in place of the URL's host, so both are checked.
		for _, host := range targetHosts(req) {
			if err := guard.CheckProxiedHost(req.Context(), host); err != nil {
				return nil, err
			}
		}
		if d != nil {
			d.proxied.Store(true)
		}
		return u, nil
	}

	guarded := guard.DialContext(nil)
	plain := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if dialIsProxied(ctx) {
			return plain.DialContext(ctx, network, addr)
		}
		return guarded(ctx, network, addr)
	}
	return &envProxyTransport{inner: tr}
}

// targetHosts returns the hosts a proxy may be told to connect to for req.
func targetHosts(req *http.Request) []string {
	hosts := []string{req.URL.Hostname()}
	if req.Host != "" {
		if h := hostnameOf(req.Host); !strings.EqualFold(h, hosts[0]) {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// hostnameOf strips any port from a Host header value. A bare IPv6 literal
// ("::ffff:a00:1") has no port; url.URL would read its last group as one.
func hostnameOf(hostport string) string {
	if net.ParseIP(hostport) != nil {
		return hostport
	}
	u := url.URL{Host: hostport}
	return u.Hostname()
}

func (t *envProxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := context.WithValue(req.Context(), proxyDecisionKey{}, &proxyDecision{})
	return t.inner.RoundTrip(req.WithContext(ctx))
}

// CloseIdleConnections lets http.Client release the wrapped transport's
// connections.
func (t *envProxyTransport) CloseIdleConnections() {
	t.inner.CloseIdleConnections()
}

// envProxyApplies reports whether a request to scheme://host[:port] would go
// through a proxy taken from the environment rather than one configured on the
// settings. port may be empty.
func (s Settings) envProxyApplies(scheme, host, port string) bool {
	if scheme != "http" && scheme != "https" {
		return false
	}
	if s.MatchProxy(host) != nil {
		return false
	}
	proxy := http.ProxyFromEnvironment
	if s.HTTPTransport != nil {
		proxy = s.HTTPTransport.Proxy
	}
	if proxy == nil {
		return false
	}
	authority := host
	switch {
	case port != "":
		authority = net.JoinHostPort(host, port)
	case strings.Contains(host, ":"):
		authority = "[" + host + "]" // a bare IPv6 literal
	}
	u, err := proxy(&http.Request{URL: &url.URL{Scheme: scheme, Host: authority}})
	return err == nil && u != nil
}
