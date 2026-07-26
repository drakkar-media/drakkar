package privacy

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// SOCKS5Config describes a SOCKS5 upstream proxy. Password is optional.
type SOCKS5Config struct {
	Host           string
	Port           int
	Username       string
	Password       string
	TimeoutSeconds int
}

// SOCKS5Dialer routes protected traffic through a configured SOCKS5 proxy.
// Hostnames are forwarded to the proxy unresolved (RFC 1928 ATYP_DOMAINNAME)
// so DNS resolution happens proxy-side, never on the host.
type SOCKS5Dialer struct {
	dialer  ContextDialer
	timeout time.Duration
}

// NewSOCKS5Dialer builds a SOCKS5Dialer from cfg.
//
// It validates that Host and Port are present, defaults TimeoutSeconds to 15
// when unset or invalid, and fails fast if the underlying proxy dialer does
// not support DialContext (golang.org/x/net/proxy can return a dialer that
// only implements the plain, non-context Dial).
func NewSOCKS5Dialer(cfg SOCKS5Config) (*SOCKS5Dialer, error) {
	// Trim accidental leading/trailing whitespace from pasted credentials --
	// a stray trailing space or newline from a copy-paste produces a
	// same-looking-but-wrong username/password that the proxy rejects with
	// an auth failure indistinguishable, from the UI, from genuinely wrong
	// credentials.
	host := strings.TrimSpace(cfg.Host)
	username := strings.TrimSpace(cfg.Username)
	password := strings.TrimSpace(cfg.Password)

	if host == "" || cfg.Port <= 0 {
		return nil, fmt.Errorf("socks5: host and port are required")
	}
	timeoutSeconds := cfg.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 15
	}
	timeout := time.Duration(timeoutSeconds) * time.Second

	var auth *proxy.Auth
	if username != "" {
		auth = &proxy.Auth{User: username, Password: password}
	}

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", cfg.Port))
	forward := &net.Dialer{Timeout: timeout}
	base, err := proxy.SOCKS5("tcp", addr, auth, forward)
	if err != nil {
		return nil, fmt.Errorf("socks5: build dialer: %w", err)
	}
	ctxDialer, ok := base.(ContextDialer)
	if !ok {
		return nil, fmt.Errorf("socks5: proxy dialer does not support DialContext")
	}
	return &SOCKS5Dialer{dialer: ctxDialer, timeout: timeout}, nil
}

// DialContext dials address through the configured SOCKS5 proxy, bounding
// each attempt by d.timeout regardless of ctx's own deadline.
//
// Retries once on failure. Many consumer SOCKS5 proxy hostnames (VPN
// providers included) resolve to a rotating pool of backend servers behind
// one DNS name; Go's own multi-address dial fallback only helps when the
// initial TCP connect fails, not when a specific backend accepts the TCP
// connection and then resets mid-handshake (confirmed live against a real
// provider: one backend node behind the same hostname consistently reset
// the connection during the SOCKS negotiate/auth exchange while others
// worked). A fresh attempt re-resolves the hostname and may land on a
// healthy node. Still exclusively via SOCKS5 -- never falls back to Direct.
func (d *SOCKS5Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d == nil || d.dialer == nil {
		return nil, fmt.Errorf("socks5: not configured")
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		dialCtx, cancel := context.WithTimeout(ctx, d.timeout)
		conn, err := d.dialer.DialContext(dialCtx, network, address)
		cancel()
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}
