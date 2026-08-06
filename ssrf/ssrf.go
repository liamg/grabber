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
	// Internal blocks loopback, RFC1918 private ranges, CGNAT (100.64.0.0/10),
	// IPv6 ULA, link-local (including the 169.254.169.254 metadata endpoint),
	// multicast, and the unspecified address.
	Internal
	// Custom delegates the decision to a caller-supplied predicate.
	Custom
)

// Guard decides whether outbound connections to a resolved IP are permitted.
// The zero Level (Default) resolves to Internal.
type Guard struct {
	level      Level
	custom     func(net.IP) bool
	allowHosts []string     // exact hostnames (case-insensitive)
	allowIPs   []net.IP     // exact IP literals
	allowNets  []*net.IPNet // CIDR ranges
}

// New returns a Guard for the given level. custom is only consulted when level
// is Custom. A Default level resolves to Internal.
//
// allow lists hosts that bypass the guard entirely. Each entry may be a
// hostname (matched case-insensitively against the target host), an IP literal,
// or a CIDR range (matched against the resolved IP). Unparseable entries are
// ignored.
func New(level Level, custom func(net.IP) bool, allow ...string) *Guard {
	if level == Default {
		level = Internal
	}
	g := &Guard{level: level, custom: custom}
	for _, entry := range allow {
		entry = strings.TrimSpace(entry)
		switch {
		case entry == "":
			continue
		case strings.Contains(entry, "/"):
			if _, n, err := net.ParseCIDR(entry); err == nil {
				g.allowNets = append(g.allowNets, n)
			}
		default:
			if ip := net.ParseIP(entry); ip != nil {
				g.allowIPs = append(g.allowIPs, ip)
			} else {
				g.allowHosts = append(g.allowHosts, entry)
			}
		}
	}
	return g
}

// hostAllowed reports whether host is on the allowlist by name.
func (g *Guard) hostAllowed(host string) bool {
	for _, h := range g.allowHosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// HostAllowed reports whether host bypasses the guard by name (see the allow
// list passed to New). It is exported for callers that dial outside DialContext
// - the connect probe, for instance - and need to mirror its host exemption.
func (g *Guard) HostAllowed(host string) bool {
	return g.hostAllowed(host)
}

// ipAllowed reports whether ip is on the allowlist by literal or CIDR.
func (g *Guard) ipAllowed(ip net.IP) bool {
	for _, a := range g.allowIPs {
		if a.Equal(ip) {
			return true
		}
	}
	for _, n := range g.allowNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Enabled reports whether the guard blocks anything.
func (g *Guard) Enabled() bool {
	return g != nil && g.level != None
}

// cgnat is 100.64.0.0/10 (RFC 6598, carrier-grade NAT). Go's IsPrivate does
// not include it, but it is only reachable inside a provider network —
// Tailscale tailnets and EKS/GKE secondary pod CIDRs live here.
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// Blocked reports whether ip must not be dialed. A nil IP is treated as blocked
// so the guard fails closed.
func (g *Guard) Blocked(ip net.IP) bool {
	if !g.Enabled() {
		return false
	}
	if ip == nil {
		return true
	}
	if g.ipAllowed(ip) {
		return false
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
			cgnat.Contains(ip) || // 100.64.0.0/10 (RFC 6598, CGNAT)
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
// this avoids false positives on split-horizon DNS).
//
// CheckHost fails closed on anything it cannot resolve itself: the protocols
// behind it fetch through their own transport or a subprocess, whose resolver
// (libc) accepts inputs the pure-Go path does not — non-canonical IPv4
// literals via inet_aton, plus nsswitch sources like mDNS — so "we couldn't
// resolve it" is not proof the fetch can't reach a blocked address.
func (g *Guard) CheckHost(ctx context.Context, host string) error {
	if !g.Enabled() || host == "" || g.hostAllowed(host) {
		return nil
	}

	if ip := net.ParseIP(host); ip != nil {
		if g.Blocked(ip) {
			return &BlockedAddressError{Host: host, IP: ip}
		}
		return nil
	}

	// A numeric host that ParseIP rejects is a non-canonical IPv4 literal:
	// octal (0177.0.0.1), hex (0x7f.0.0.1), plain decimal (2130706433), or
	// short form (127.1). libc's inet_aton decodes all of these to real
	// addresses, so reject them outright instead of falling through to a DNS
	// lookup that cannot succeed.
	if isNumericHost(host) {
		return fmt.Errorf("ssrf guard: refusing non-canonical IP literal %q", host)
	}

	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("ssrf guard: cannot resolve host %q: %w", host, err)
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

// isNumericHost reports whether host is made of 1-4 dot-separated parts that
// are all decimal, octal (leading 0) or hex (0x prefix) numbers — the grammar
// libc's inet_aton accepts as an IPv4 literal. A canonical dotted quad parses
// with net.ParseIP before this is consulted, so a match here means a
// non-canonical literal.
func isNumericHost(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return false
	}
	for _, p := range parts {
		if !isNumericPart(p) {
			return false
		}
	}
	return true
}

func isNumericPart(s string) bool {
	if len(s) > 2 && (strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X")) {
		s = s[2:]
		for _, r := range s {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
				return false
			}
		}
		return true
	}
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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
	guarded.Control = g.Control

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if (exempt != nil && exempt(addr)) || g.hostAllowed(hostOf(addr)) {
			return plain.DialContext(ctx, network, addr)
		}
		return guarded.DialContext(ctx, network, addr)
	}
}

// Control is a net.Dialer.Control callback that rejects a connection whose
// resolved address is blocked. The dialer invokes it after DNS resolution with
// the concrete IP:port, which is what gives the check its DNS-rebinding and
// redirect safety. It is used by DialContext and by callers that dial with
// their own net.Dialer (the connect probe) and want the same guarantee.
func (g *Guard) Control(_, address string, _ syscall.RawConn) error {
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

// hostOf returns the host portion of a "host:port" address, or the input
// unchanged if it has no port.
func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
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
