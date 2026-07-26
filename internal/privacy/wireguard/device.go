package wireguard

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	wgconn "golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// Tunnel is a live userspace WireGuard device bound to a gVisor netstack —
// an in-process TUN with no host routing-table or /dev/net/tun involvement.
type Tunnel struct {
	dev *device.Device
	net *netstack.Net
	cfg *Config
}

// Start brings up a userspace WireGuard tunnel for cfg. On any failure no
// partial device is left running — the caller can safely retry or fall back
// to the previously active route.
func Start(cfg *Config) (*Tunnel, error) {
	localAddrs := make([]netip.Addr, 0, len(cfg.Address))
	for _, p := range cfg.Address {
		localAddrs = append(localAddrs, p.Addr())
	}
	dnsAddrs := append([]netip.Addr(nil), cfg.DNS...)

	tunDev, tnet, err := netstack.CreateNetTUN(localAddrs, dnsAddrs, cfg.MTU)
	if err != nil {
		return nil, fmt.Errorf("wireguard: create netstack tun: %w", err)
	}

	logger := device.NewLogger(device.LogLevelError, "wireguard: ")
	dev := device.NewDevice(tunDev, wgconn.NewDefaultBind(), logger)

	if err := dev.IpcSet(uapiConfig(cfg)); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard: configure device: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard: bring device up: %w", err)
	}

	return &Tunnel{dev: dev, net: tnet, cfg: cfg}, nil
}

// Close tears down the device and releases every goroutine/socket it owns.
// Safe to call once; the manager guarantees a Tunnel is closed exactly once.
func (t *Tunnel) Close() {
	if t == nil || t.dev == nil {
		return
	}
	t.dev.Close()
}

// DialContext satisfies privacy.ContextDialer, routing the dial through the
// live tunnel's userspace netstack. There is no fallback path: if the
// tunnel isn't up, the underlying netstack call simply fails.
func (t *Tunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return t.net.DialContext(ctx, network, address)
}

// Net exposes the netstack handle used to build a ContextDialer.
func (t *Tunnel) Net() *netstack.Net {
	return t.net
}

// uapiConfig renders cfg into the UAPI text protocol device.IpcSet expects.
// Never logged — callers must treat the returned string itself as a secret.
func uapiConfig(cfg *Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", cfg.PrivateKeyHex)
	for _, peer := range cfg.Peers {
		fmt.Fprintf(&b, "public_key=%s\n", peer.PublicKeyHex)
		if peer.PresharedKeyHex != "" {
			fmt.Fprintf(&b, "preshared_key=%s\n", peer.PresharedKeyHex)
		}
		fmt.Fprintf(&b, "endpoint=%s\n", peer.Endpoint)
		for _, ip := range peer.AllowedIPs {
			fmt.Fprintf(&b, "allowed_ip=%s\n", ip.String())
		}
		if peer.PersistentKeepalive > 0 {
			fmt.Fprintf(&b, "persistent_keepalive_interval=%s\n", strconv.Itoa(peer.PersistentKeepalive))
		}
	}
	return b.String()
}
