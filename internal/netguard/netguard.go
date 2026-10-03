// Package netguard provides SSRF-safe HTTP clients. Unlike a resolve-then-fetch
// check, validation happens on the connected socket address inside the dialer's
// Control hook, so DNS rebinding and redirect-to-internal attacks are blocked.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrPrivateAddress is returned when a connection targets a non-public address.
var ErrPrivateAddress = errors.New("access to private/internal network address is prohibited")

// Options controls client construction.
type Options struct {
	AllowPrivate  bool
	ForceIPv6     bool
	SourceAddress string
	Timeout       time.Duration // whole-request timeout; 0 = none (streaming)
}

// IsPrivate reports whether ip is not a publicly routable unicast address.
func IsPrivate(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || !ip.IsGlobalUnicast() ||
		isCGNAT(ip)
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func isCGNAT(ip netip.Addr) bool { return cgnat.Contains(ip) }

// NewTransport builds an http.Transport that enforces Options.
func NewTransport(o Options) *http.Transport {
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if o.SourceAddress != "" {
		if ip := net.ParseIP(o.SourceAddress); ip != nil {
			d.LocalAddr = &net.TCPAddr{IP: ip}
		}
	}
	if !o.AllowPrivate {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return fmt.Errorf("invalid dial address %q: %w", address, err)
			}
			if IsPrivate(ip) {
				return fmt.Errorf("%w: %s", ErrPrivateAddress, ip)
			}
			return nil
		}
	}
	network := "tcp"
	if o.ForceIPv6 {
		network = "tcp6"
	}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
}

// NewClient returns an HTTP client using NewTransport with a redirect cap.
func NewClient(o Options) *http.Client {
	return &http.Client{
		Transport: NewTransport(o),
		Timeout:   o.Timeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}
}

// CheckURL is an early, best-effort validation for user-supplied URLs: it
// rejects non-http(s) schemes and hosts that resolve to non-public addresses.
// The dialer hook remains the authoritative enforcement point.
func CheckURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("invalid URL %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		if IsPrivate(ip) {
			return fmt.Errorf("%w: %s", ErrPrivateAddress, raw)
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil {
		return fmt.Errorf("resolve %q: %w", u.Hostname(), err)
	}
	for _, a := range addrs {
		if IsPrivate(a) {
			return fmt.Errorf("%w: %s resolves to %s", ErrPrivateAddress, u.Hostname(), a)
		}
	}
	return nil
}
