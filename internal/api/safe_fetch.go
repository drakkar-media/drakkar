package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
)

const (
	safeFetchTimeout       = 30 * time.Second
	safeFetchDialTimeout   = 10 * time.Second
	safeFetchMaxBodyBytes  = config.DefaultNZBUploadLimitBytes
	safeFetchMaxConcurrent = 4
)

var safeFetchSlots = make(chan struct{}, safeFetchMaxConcurrent)

// Explicit special-use ranges cover addresses that IsPrivate alone misses,
// including carrier-grade NAT, benchmarks, documentation, and transition nets.
var nonPublicFetchPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// rejectSSRFIP reports whether ip is a non-public address that a
// server-side fetch of a user-supplied URL must never be allowed to reach.
func rejectSSRFIP(ip net.IP) error {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return fmt.Errorf("refusing to connect to invalid address %q", ip)
	}
	addr = addr.Unmap()
	for _, prefix := range nonPublicFetchPrefixes {
		if prefix.Contains(addr) {
			return fmt.Errorf("refusing to connect to non-public address %s", addr)
		}
	}
	return nil
}

func fetchAuthority(host, port string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return net.JoinHostPort(host, port)
}

func trustedFetchAuthorities(rawURLs []string) map[string]struct{} {
	authorities := make(map[string]struct{}, len(rawURLs))
	for _, rawURL := range rawURLs {
		u, err := url.Parse(strings.TrimSpace(rawURL))
		if err != nil || u.Hostname() == "" {
			continue
		}
		port := u.Port()
		switch {
		case port != "":
		case strings.EqualFold(u.Scheme, "http"):
			port = "80"
		case strings.EqualFold(u.Scheme, "https"):
			port = "443"
		default:
			continue
		}
		authorities[fetchAuthority(u.Hostname(), port)] = struct{}{}
	}
	return authorities
}

// safeFetchClient returns an http.Client whose dialer resolves the host
// itself and validates every resolved IP before connecting to that exact
// IP. A hostname-based pre-check (resolve once to reject private IPs, then
// let the transport dial the hostname separately) is bypassable via DNS
// rebinding: the name can resolve to a public IP for the check and a
// private one for the real connection a moment later. Resolving once here
// and dialing the validated IP directly closes that gap.
func safeFetchClient(trustedUpstreamURLs []string) *http.Client {
	dialer := &net.Dialer{Timeout: safeFetchDialTimeout}
	trustedAuthorities := trustedFetchAuthorities(trustedUpstreamURLs)
	return &http.Client{
		Timeout: safeFetchTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				_, trusted := trustedAuthorities[fetchAuthority(host, port)]
				if ip := net.ParseIP(host); ip != nil {
					if !trusted {
						if err := rejectSSRFIP(ip); err != nil {
							return nil, err
						}
					}
					return dialer.DialContext(ctx, network, addr)
				}
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
				if err != nil {
					return nil, fmt.Errorf("resolve host: %w", err)
				}
				if len(ips) == 0 {
					return nil, fmt.Errorf("resolve host: no addresses for %s", host)
				}
				if !trusted {
					for _, ip := range ips {
						if err := rejectSSRFIP(ip); err != nil {
							return nil, err
						}
					}
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
			},
		},
	}
}

// fetchRemoteURL performs a GET on rawURL with SSRF protection (validated at
// actual connection time, not just at an earlier hostname lookup), a request
// timeout, and a body size cap. Used by any handler that fetches a
// user-supplied remote URL (NZB import, SABnzbd addurl shim) — several of
// these previously each hand-rolled their own (inconsistent) subset of these
// protections.
func fetchRemoteURL(ctx context.Context, rawURL string) ([]byte, error) {
	return fetchRemoteURLFromUpstreams(ctx, rawURL, nil)
}

func fetchRemoteURLFromUpstreams(ctx context.Context, rawURL string, trustedUpstreamURLs []string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported url scheme: %s", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, errors.New("invalid url: host required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "Drakkar/1.0")
	select {
	case safeFetchSlots <- struct{}{}:
		defer func() { <-safeFetchSlots }()
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for remote fetch slot: %w", ctx.Err())
	}
	client := safeFetchClient(trustedUpstreamURLs)
	// Trust policy is captured by a request-specific transport. Closing idle
	// connections prevents that transport and its socket goroutines lingering.
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("remote returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > safeFetchMaxBodyBytes {
		return nil, fmt.Errorf("remote body exceeds %d bytes", safeFetchMaxBodyBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, safeFetchMaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read url body: %w", err)
	}
	if int64(len(body)) > safeFetchMaxBodyBytes {
		return nil, fmt.Errorf("remote body exceeds %d bytes", safeFetchMaxBodyBytes)
	}
	return body, nil
}
