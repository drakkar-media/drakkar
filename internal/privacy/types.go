// Package privacy implements selective privacy routing for Usenet/NNTP and
// NZB indexer HTTP traffic: Direct, SOCKS5, or an in-process userspace
// WireGuard tunnel. No other Drakkar traffic (Plex/Jellyfin/metadata/local)
// is ever routed through this package.
package privacy

import (
	"context"
	"net"
)

// Mode selects which route protected traffic uses. Exactly one mode is
// active at a time — there is no independent enable/disable per route.
type Mode string

const (
	ModeDirect    Mode = "direct"
	ModeSOCKS5    Mode = "socks5"
	ModeWireGuard Mode = "wireguard"
)

// ValidMode reports whether m is one of the three supported modes.
func ValidMode(m Mode) bool {
	switch m {
	case ModeDirect, ModeSOCKS5, ModeWireGuard:
		return true
	default:
		return false
	}
}

// ContextDialer is the shape both the NNTP client and net/http transports
// consume. Every implementation in this package (Direct/SOCKS5/WireGuard)
// satisfies it, so callers never need an adapter layer.
type ContextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// ContextDialerFunc adapts a plain function to ContextDialer.
type ContextDialerFunc func(ctx context.Context, network, address string) (net.Conn, error)

// DialContext calls f, satisfying ContextDialer.
func (f ContextDialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}
