// Package ssrf provides a server-side request forgery (SSRF) guard for
// grabber's outbound connections. When grabber fetches a URL that an untrusted
// party controls (for example a Terraform module source in a CI run), the guard
// prevents it from reaching loopback, link-local (including the cloud metadata
// endpoint 169.254.169.254), and private addresses that are only reachable from
// inside the network.
//
// There are two layers:
//
//   - DialContext guards the transport used by the HTTP and OCI protocols. The
//     check runs on the resolved IP at connect time, so it catches both DNS
//     rebinding ("evil.com resolves to 127.0.0.1") and redirect-to-internal
//     (each redirect re-dials and is re-checked).
//   - CheckHost is a pre-fetch check for protocols that use their own transport
//     (git via go-git, hg via a subprocess) and so never dial through DialContext.
package ssrf

import (
	"context"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
)

// Level selects how much of the address space the guard blocks.
type Level int

const (
	// Default is the zero value; it resolves to Internal so the guard is on
	// unless a caller explicitly opts out with None.
	Default Level = iota
	// None disables the guard entirely.
	None
	// Loopback blocks only loopback (127.0.0.0/8, ::1) and the unspecified
	// address (0.0.0.0, ::).
	Loopback
	// Internal blocks loopback, RFC1918 private ranges, IPv6 ULA, link-local
	// (including the 169.254.169.254 metadata endpoint), multicast, and the
	// unspecified address.
	Internal
	// Custom delegates the decision to a caller-supplied predicate.
	Custom
)

// Guard decides whether outbound connections to a resolved IP are permitted.
// The zero Level (Default) resolves to Internal.
type Guard struct {
	level  Level
	custom func(net.IP) bool
}

// New returns a Guard for the given level. custom is only consulted when level
// is Custom. A Default level resolves to Internal.
func New(level Level, custom func(net.IP) bool) *Guard {
	if level == Default {
		level = Internal
	}
	return &Guard{level: level, custom: custom}
}

// Enabled reports whether the guard blocks anything.
func (g *Guard) Enabled() bool {
	return g != nil && g.level != None
}

// Blocked reports whether ip must not be dialed. A nil IP is treated as blocked
// so the guard fails closed.
func (g *Guard) Blocked(ip net.IP) bool {
	if !g.Enabled() {
		return false
	}
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	switch g.level {
	case Custom:
		return g.custom != nil && g.custom(ip)
	case Loopback:
		return ip.IsLoopback() || ip.IsUnspecified()
	case Internal:
		return ip.IsLoopback() || // 127.0.0.0/8, ::1
			ip.IsPrivate() || // 10/8, 172.16/12, 192.168/16, fc00::/7
			ip.IsLinkLocalUnicast() || // 169.254.0.0/16 (metadata), fe80::/10
			ip.IsLinkLocalMulticast() ||
			ip.IsInterfaceLocalMulticast() ||
			ip.IsMulticast() ||
			ip.IsUnspecified() // 0.0.0.0, ::
	default:
		return false
	}
}

// BlockedAddressError is returned when the guard rejects a target.
type BlockedAddressError struct {
	Host string
	IP   net.IP
}

func (e *BlockedAddressError) Error() string {
	if e.Host != "" && (e.IP == nil || e.Host != e.IP.String()) {
		return fmt.Sprintf("refusing to connect to %s (resolves to blocked address %s)", e.Host, e.IP)
	}
	return fmt.Sprintf("refusing to connect to blocked address %s", e.IP)
}

// CheckHost resolves host (a hostname or IP literal) and returns a
// BlockedAddressError if it — or every address it resolves to — is blocked. A
// host that also resolves to at least one allowed address is permitted (the
// dialer guard still rejects any blocked address it is later asked to dial, and
// this avoids false positives on split-horizon DNS). A DNS failure returns nil:
// there is nothing to safely block, and the fetch fails on its own.
func (g *Guard) CheckHost(ctx context.Context, host string) error {
	if !g.Enabled() || host == "" {
		return nil
	}

	if ip := net.ParseIP(host); ip != nil {
		if g.Blocked(ip) {
			return &BlockedAddressError{Host: host, IP: ip}
		}
		return nil
	}

	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil
	}

	var firstBlocked net.IP
	for _, a := range addrs {
		if !g.Blocked(a.IP) {
			return nil
		}
		if firstBlocked == nil {
			firstBlocked = a.IP
		}
	}
	if firstBlocked == nil {
		return nil
	}
	return &BlockedAddressError{Host: host, IP: firstBlocked}
}

// DialContext returns a dial function that rejects connections to blocked
// addresses, for use as an http.Transport.DialContext. The check runs in the
// dialer's Control hook, which fires after DNS resolution but before connect
// with the concrete IP — this is what gives DNS-rebinding and redirect safety.
//
// exempt, when non-nil and true for a dial address, skips the check. Callers use
// it to exempt a trusted proxy: when proxying, the transport dials the proxy
// (not the attacker-chosen target), so the proxy's own address must be allowed.
//
// If the guard is disabled the returned function is a plain dialer.
func (g *Guard) DialContext(exempt func(addr string) bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	plain := newDialer()
	if !g.Enabled() {
		return plain.DialContext
	}

	guarded := newDialer()
	guarded.Control = func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			host = address
		}
		ip := net.ParseIP(host)
		if ip == nil {
			// Control is always called with a resolved IP:port; a parse failure
			// is unexpected, so fail closed rather than risk a bypass.
			return fmt.Errorf("ssrf guard: cannot parse dial address %q", address)
		}
		if g.Blocked(ip) {
			return &BlockedAddressError{Host: host, IP: ip}
		}
		return nil
	}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if exempt != nil && exempt(addr) {
			return plain.DialContext(ctx, network, addr)
		}
		return guarded.DialContext(ctx, network, addr)
	}
}

// ExemptHost returns an exempt predicate that matches dial addresses whose host
// equals host (case-insensitive), or nil if host is empty.
func ExemptHost(host string) func(addr string) bool {
	if host == "" {
		return nil
	}
	return func(addr string) bool {
		h, _, err := net.SplitHostPort(addr)
		if err != nil {
			h = addr
		}
		return strings.EqualFold(h, host)
	}
}

func newDialer() *net.Dialer {
	return &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
}
