package ssrf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

// proxiedLookupTimeout bounds the name lookup in CheckProxiedHost so a resolver
// that cannot answer (no DNS on the runner) does not stall a request for long.
// Answers and failures are cached (see lookupCache). It is a var so tests can
// shorten it.
var proxiedLookupTimeout = 2 * time.Second

// lookupIPAddr is a var so tests can control name resolution.
var lookupIPAddr = net.DefaultResolver.LookupIPAddr

// CheckProxiedHost is the pre-check for a request that is about to be sent
// through an environment proxy. The proxy connects to the target itself, so the
// dial guard never sees the target's address and this is the only check the
// target gets. It is stricter than CheckHost:
//
//   - A non-canonical IPv4 literal (2852039166, 127.1, 0x7f.0.0.1, 0177.0.0.1) is
//     refused outright. net.ParseIP rejects these and the pure-Go resolver treats
//     them as hostnames, but a proxy using libc inet_aton reads them as addresses.
//   - The host is normalized first, at least as aggressively as net/http does
//     when it builds the request (Unicode to ASCII, trailing dot removed), so a
//     host that would reach the proxy as an internal address is caught. A
//     non-ASCII host is checked in both ASCII forms net/http can send (see
//     asciiForms). A host with an empty label ("10.0.0..1") is refused: no real
//     host has one.
//   - A name is refused if any address it resolves to is blocked, because a proxy
//     may pick that one (CheckHost allows a name with one allowed address). A
//     lookup that fails is refused too, unless the resolver does not know the name
//     or this machine appears to have no working DNS (see lookupFailureAllowed).
//   - NAT64 (64:ff9b::/96), 6to4 (2002::/16) and IPv4-compatible (::a.b.c.d)
//     addresses are judged by the IPv4 address they carry.
//
// A name the caller's resolver does not know is allowed: where only the proxy has
// DNS that is every external host. The proxy can still resolve such a name to an
// internal address, or be rebound after this check, and whoever runs a domain's
// DNS can answer the caller and the proxy differently; that risk is inherent to
// using a proxy and is not closed here. A lookup that fails some other way, a
// timeout or a server failure, is refused unless this machine appears to have no working DNS
// (a lookup of a well-known public name fails too).
func (g *Guard) CheckProxiedHost(ctx context.Context, host string) error {
	if !g.Enabled() || host == "" {
		return nil
	}

	forms, err := asciiForms(host)
	if err != nil {
		return err
	}
	for _, form := range forms {
		if err := g.checkProxiedASCII(ctx, form); err != nil {
			return err
		}
	}
	return nil
}

// asciiForms returns the ASCII spellings of host a proxy can receive, each with
// an absolute name's trailing dot removed. A non-ASCII host has two: net/http
// sends the CONNECT line of an https:// request in the IDNA lookup form (fullwidth
// digits and ideographic full stops become "127.0.0.1"), but builds the
// absolute URI of a plain http:// request with a profile that does no mapping, so
// the two can name different hosts. Both are returned, and a host that cannot be
// converted is refused.
func asciiForms(host string) ([]string, error) {
	forms := []string{host}
	if !isASCII(host) {
		lookup, err := idna.Lookup.ToASCII(host)
		if err != nil {
			return nil, fmt.Errorf("ssrf guard: refusing invalid host %q: %w", host, err)
		}
		forms = []string{lookup}
		punycode, err := idna.ToASCII(host)
		if err != nil {
			return nil, fmt.Errorf("ssrf guard: refusing invalid host %q: %w", host, err)
		}
		if punycode != lookup {
			forms = append(forms, punycode)
		}
	}

	for i, form := range forms {
		if len(form) > 1 {
			form = strings.TrimSuffix(form, ".")
		}
		if strings.HasPrefix(form, ".") || strings.HasSuffix(form, ".") || strings.Contains(form, "..") {
			return nil, fmt.Errorf("ssrf guard: refusing invalid host %q", form)
		}
		forms[i] = form
	}
	return forms, nil
}

// dnsProbeName is a public name with a real answer, used to tell whether this
// machine has working external DNS. It is absolute (trailing dot) so search
// domains do not alter it. A reserved name such as .invalid is no use here:
// systemd-resolved and Unbound answer it locally without asking upstream, so it
// would look like working DNS on a runner whose upstream is down.
const dnsProbeName = "example.com."

// lookupFailureAllowed reports whether a failed lookup of a proxied host may
// proceed to the proxy. A name this machine's resolver does not know ("no such
// host") is allowed: where only the proxy has DNS, that is every external host.
// Any other failure, such as a timeout or a server failure, is allowed only when
// a lookup of a well-known public name fails as well, which this treats as "the
// machine has no external DNS". Otherwise whoever controls a domain's DNS could
// make the lookup stall or fail to skip the check of what it resolves to. This
// is a heuristic: it cannot tell a dead resolver from one that only refuses some
// names.
func lookupFailureAllowed(ctx context.Context, err error) bool {
	if isNotFound(err) {
		return true
	}
	_, probeErr := sharedLookups.lookup(ctx, dnsProbeName, proxiedLookupTimeout)
	return probeErr != nil
}

func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// checkProxiedASCII is CheckProxiedHost for one normalized ASCII spelling.
func (g *Guard) checkProxiedASCII(ctx context.Context, host string) error {
	if g.hostAllowed(host) {
		return nil
	}

	if ip := net.ParseIP(host); ip != nil {
		if g.blockedWithEmbedded(ip) {
			return &BlockedAddressError{Host: host, IP: ip}
		}
		return nil
	}

	if isNumericHost(host) {
		return fmt.Errorf("ssrf guard: refusing non-canonical IP literal %q", host)
	}

	addrs, err := sharedLookups.lookup(ctx, strings.ToLower(host), proxiedLookupTimeout)
	if err != nil {
		if ctx.Err() == nil && !lookupFailureAllowed(ctx, err) {
			return fmt.Errorf("ssrf guard: cannot check host %q: %w", host, err)
		}
		return nil
	}
	for _, a := range addrs {
		if g.blockedWithEmbedded(a.IP) {
			return &BlockedAddressError{Host: host, IP: a.IP}
		}
	}
	return nil
}

// blockedWithEmbedded is Blocked, also judging an IPv6 translation address by
// the IPv4 address it carries.
func (g *Guard) blockedWithEmbedded(ip net.IP) bool {
	if g.ipAllowed(ip) {
		return false
	}
	if g.Blocked(ip) || (g.level == Internal && ip.To4() == nil && nat64Local.Contains(ip)) {
		return true
	}
	for _, v4 := range embeddedIPv4(ip.To16()) {
		if g.Blocked(v4) {
			return true
		}
	}
	return false
}

// nat64Local is 64:ff9b:1::/48 (RFC 8215), a local-use NAT64 prefix whose
// embedded IPv4 address position depends on the prefix length. It has no
// business in a module source, so it is refused whole.
var nat64Local = &net.IPNet{IP: net.ParseIP("64:ff9b:1::"), Mask: net.CIDRMask(48, 128)}

// embeddedIPv4 returns the IPv4 addresses carried by an IPv6 address in one of
// the well-known translation forms, or nil if ip is not one: NAT64
// (64:ff9b::/96), 6to4 (2002::/16), Teredo (2001::/32, both the server and the
// obfuscated client address) or IPv4-compatible (::a.b.c.d). IPv4-mapped
// (::ffff:a.b.c.d) is already normalized by To4.
func embeddedIPv4(ip net.IP) []net.IP {
	if len(ip) != net.IPv6len || ip.To4() != nil {
		return nil
	}

	zero := func(b []byte) bool {
		for _, v := range b {
			if v != 0 {
				return false
			}
		}
		return true
	}

	switch {
	case ip[0] == 0x00 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b && zero(ip[4:12]):
		return []net.IP{ip[12:16]}
	case ip[0] == 0x20 && ip[1] == 0x02:
		return []net.IP{ip[2:6]}
	case ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0 && ip[3] == 0:
		client := net.IP{ip[12] ^ 0xff, ip[13] ^ 0xff, ip[14] ^ 0xff, ip[15] ^ 0xff}
		return []net.IP{ip[4:8], client}
	case zero(ip[:12]):
		return []net.IP{ip[12:16]}
	}
	return nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
