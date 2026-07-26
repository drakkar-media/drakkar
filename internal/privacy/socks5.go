package privacy

import (
	"context"
	"fmt"
	"net"
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
	if cfg.Host == "" || cfg.Port <= 0 {
		return nil, fmt.Errorf("socks5: host and port are required")
	}
	timeoutSeconds := cfg.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 15
	}
	timeout := time.Duration(timeoutSeconds) * time.Second

	var auth *proxy.Auth
	if cfg.Username != "" {
		auth = &proxy.Auth{User: cfg.Username, Password: cfg.Password}
	}

	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
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
// the dial by d.timeout regardless of ctx's own deadline.
func (d *SOCKS5Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d == nil || d.dialer == nil {
		return nil, fmt.Errorf("socks5: not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.dialer.DialContext(ctx, network, address)
}
