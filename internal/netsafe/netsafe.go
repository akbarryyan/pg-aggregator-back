// Package netsafe keeps outbound requests to merchant-supplied URLs from
// reaching internal infrastructure (SSRF). Callback and webhook URLs are
// merchant input, but the server makes the request, from inside our network.
package netsafe

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
)

var (
	// ErrInvalidCallbackURL means a URL failed the storage-time rules.
	ErrInvalidCallbackURL = errors.New("invalid callback url")
	// ErrBlockedAddress means a connection was refused because the resolved
	// destination address is not public.
	ErrBlockedAddress = errors.New("destination address not allowed")
)

const maxURLLength = 2048

// blockedPrefixes are ranges netip's helper methods don't already cover:
// reserved, documentation, benchmarking, CGNAT, and IPv6 forms that embed
// an IPv4 address (NAT64, 6to4, Teredo).
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	// ::/96 (IPv4-compatible) and ::ffff:0:0:0/96 (SIIT, RFC 6145) embed IPv4 but are not
	// the mapped form that Unmap handles.
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::ffff:0:0:0/96"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
}

// IsBlockedIP reports whether ip must never be contacted. It fails closed:
// a nil or malformed address counts as blocked. IPv4-mapped IPv6 addresses
// are judged by the IPv4 address they wrap.
func IsBlockedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// blockedHosts holds our own hostnames (API, frontend). They resolve to public
// IPs, so the IP guard can't stop a merchant from pointing a callback at us.
// Stored atomically: set once at startup, read on every validation.
var blockedHosts atomic.Pointer[[]string]

// SetBlockedHosts replaces the list of hosts (and their subdomains) that
// ValidateCallbackURL rejects in every mode. Entries may be bare hosts or full
// URLs; they are reduced to a lowercase hostname without port. Empty values,
// IP literals and localhost are ignored (other rules already cover them).
// nil or empty clears the list.
func SetBlockedHosts(hosts []string) {
	var list []string
	for _, h := range hosts {
		if n := normalizeBlockedHost(h); n != "" {
			list = append(list, n)
		}
	}
	blockedHosts.Store(&list)
}

func normalizeBlockedHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "//" + raw // let url.Parse split off port and path
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimRight(u.Hostname(), "."))
	if host == "" || host == "localhost" || net.ParseIP(host) != nil {
		return ""
	}
	return host
}

// isBlockedHost matches host (already lowercased, no trailing dot) against the
// list on label boundaries: "api.example.com" matches "example.com",
// "evilexample.com" does not.
func isBlockedHost(host string) bool {
	list := blockedHosts.Load()
	if list == nil {
		return false
	}
	for _, b := range *list {
		if host == b || strings.HasSuffix(host, "."+b) {
			return true
		}
	}
	return false
}

func invalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidCallbackURL, reason)
}

// ValidateCallbackURL applies the storage-time rules. It does no DNS
// lookup (no I/O on the request path); the connect-time check in
// NewSafeClient is what actually stops DNS tricks. allowHTTP (sandbox
// payments) relaxes only the scheme: plain http is accepted, nothing else.
// The host rules always apply, because our server can never reach a
// merchant's localhost, and allowing internal addresses would let any
// sandbox merchant probe our own network. Credentials and non-http(s)
// schemes are rejected in both modes.
func ValidateCallbackURL(raw string, allowHTTP bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return invalid("url is empty")
	}
	if len(raw) > maxURLLength {
		return invalid("url is too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse's error text embeds the URL; keep it out of ours.
		return invalid("url is malformed")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowHTTP {
			return invalid("url must use https")
		}
	default:
		return invalid("url must use https")
	}
	if u.User != nil {
		return invalid("credentials in url are not allowed")
	}
	host := u.Hostname()
	if host == "" {
		return invalid("url has no host")
	}

	host = strings.ToLower(strings.TrimRight(host, "."))
	if strings.Contains(host, "%") {
		return invalid("host zone is not allowed")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return invalid("host is not public")
	}
	if isBlockedHost(host) {
		return invalid("host is not allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		if IsBlockedIP(ip) {
			return invalid("host is not a public address")
		}
		return nil
	}
	if hostLooksNumeric(host) {
		// Decimal/hex/octal spellings (2130706433, 0x7f.0.0.1) that some
		// resolvers turn into an IP.
		return invalid("numeric host is not allowed")
	}
	if !strings.Contains(host, ".") {
		return invalid("host must be a fully qualified domain name")
	}
	return nil
}

func hostLooksNumeric(host string) bool {
	labels := strings.Split(host, ".")
	last := labels[len(labels)-1]
	if last == "" {
		return false
	}
	if strings.HasPrefix(last, "0x") || strings.HasPrefix(last, "0X") {
		return true
	}
	for _, r := range last {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// checkDialAddress is the dialer's Control hook: it runs after DNS
// resolution, right before connect, so it sees the real destination IP and
// also defeats DNS rebinding.
func checkDialAddress(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrBlockedAddress
	}
	ip := net.ParseIP(host)
	if ip == nil || IsBlockedIP(ip) {
		return ErrBlockedAddress
	}
	return nil
}

// NewSafeClient returns the client for outbound merchant callbacks. Every
// connection's destination IP is checked right before connecting, in every
// environment. Redirects are never followed (a 3xx is returned as the
// response, which callers treat as a failed delivery), and environment
// proxies are ignored so the IP check can't be bypassed through one.
func NewSafeClient(timeout time.Duration) *http.Client {
	return newClient(timeout, checkDialAddress)
}

// newClient lets tests swap the dial guard to reach loopback servers.
func newClient(timeout time.Duration, control func(network, address string, c syscall.RawConn) error) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: control}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 5 * time.Second,
			DisableKeepAlives:   true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

const maxErrorLength = 300

// DescribeError turns an error from a callback request into text that is
// safe to store and show: it drops the request URL (callback URLs often
// carry tokens) and, for refused destinations, the resolved internal IP.
func DescribeError(err error) string {
	if err == nil {
		return ""
	}
	var msg string
	var ue *url.Error
	switch {
	case errors.Is(err, ErrBlockedAddress):
		msg = ErrBlockedAddress.Error()
	case errors.As(err, &ue) && ue.Err != nil:
		msg = ue.Err.Error()
	default:
		msg = err.Error()
	}
	return storableText(msg, maxErrorLength)
}

// storableText makes s safe for a PostgreSQL TEXT column, which rejects
// invalid UTF-8 and NUL bytes: such a write failing would leave the delivery
// unrecorded. It drops both, then cuts to at most maxBytes on a character
// boundary (a byte cut could split a multibyte rune).
func storableText(s string, maxBytes int) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", "")
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
