package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const (
	safeFetchTimeout      = 30 * time.Second
	safeFetchDialTimeout  = 10 * time.Second
	safeFetchMaxBodyBytes = 200 << 20 // 200MB — generous for even large season-pack NZBs
)

// rejectSSRFIP reports whether ip is a non-public address that a
// server-side fetch of a user-supplied URL must never be allowed to reach.
func rejectSSRFIP(ip net.IP) error {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
		return fmt.Errorf("refusing to connect to non-public address %s", ip)
	}
	return nil
}

// safeFetchClient returns an http.Client whose dialer resolves the host
// itself and validates every resolved IP before connecting to that exact
// IP. A hostname-based pre-check (resolve once to reject private IPs, then
// let the transport dial the hostname separately) is bypassable via DNS
// rebinding: the name can resolve to a public IP for the check and a
// private one for the real connection a moment later. Resolving once here
// and dialing the validated IP directly closes that gap.
func safeFetchClient() *http.Client {
	dialer := &net.Dialer{Timeout: safeFetchDialTimeout}
	return &http.Client{
		Timeout: safeFetchTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				if ip := net.ParseIP(host); ip != nil {
					if err := rejectSSRFIP(ip); err != nil {
						return nil, err
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
				for _, ip := range ips {
					if err := rejectSSRFIP(ip); err != nil {
						return nil, err
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
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported url scheme: %s", u.Scheme)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "Drakkar/1.0")
	resp, err := safeFetchClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("remote returned HTTP %d", resp.StatusCode)
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
