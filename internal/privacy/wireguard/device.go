package wireguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	wgconn "golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// linkName is the fixed interface name Drakkar's WireGuard tunnel is
// created under. A single, well-known name is fine since exactly one
// tunnel exists per process (privacy.Manager holds at most one Tunnel).
const linkName = "drakkarwg0"

// wgRouteMetric is deliberately very high so the interface's AllowedIPs
// route is never selected for ordinary, unbound outbound traffic -- it
// only matters to a socket that explicitly binds itself to linkName (see
// DialContext), whose FIB lookup is restricted to routes via that device
// regardless of metric. This is what keeps Plex/DB/local/other Drakkar
// traffic on the normal default route even with an AllowedIPs of 0.0.0.0/0.
const wgRouteMetric = 999999

// Tunnel is a live WireGuard device bound to a real kernel TUN interface --
// wireguard-go still performs the encryption/handshake entirely in
// userspace (no kernel WireGuard module required), but the decrypted
// plaintext IP/TCP traffic is handed to the real Linux kernel network
// stack via the TUN device, not a userspace-reimplemented one.
//
// This replaced an earlier implementation built on
// golang.zx2c4.com/wireguard/tun/netstack (a gVisor-based userspace
// network stack, chosen because the container originally had no
// /dev/net/tun/NET_ADMIN access). That was confirmed live (2026-07-27) to
// corrupt NNTP article data under real concurrent connection load --
// yEnc CRC mismatches and "article missing" errors occurred constantly at
// 25 and even 2 concurrent connections through the tunnel, and vanished
// entirely only when serialized to exactly 1 -- almost certainly a
// demultiplexing bug in gVisor's from-scratch TCP/IP stack under
// concurrency, not a WireGuard protocol limitation (a normal kernel
// WireGuard interface handles many concurrent connections over one tunnel
// without issue, which is exactly what this real-TUN approach restores).
type Tunnel struct {
	dev      *device.Device
	tunDev   tun.Device
	linkName string
}

// Start brings up a real kernel TUN interface, configures it (address,
// MTU, a metric-999999 route per AllowedIPs -- see wgRouteMetric), and
// layers a userspace WireGuard device on top of it. On any failure no
// partial device or interface is left running -- the caller can safely
// retry or fall back to the previously active route.
//
// Requires NET_ADMIN and /dev/net/tun (granted to the Drakkar container
// specifically for this). Returns a clear, actionable error if either is
// unavailable rather than silently falling back to an unprotected route.
func Start(cfg *Config) (*Tunnel, error) {
	tunDev, err := tun.CreateTUN(linkName, cfg.MTU)
	if err != nil {
		return nil, fmt.Errorf("wireguard: create tun device (needs NET_ADMIN + /dev/net/tun): %w", err)
	}
	realName, err := tunDev.Name()
	if err != nil {
		tunDev.Close()
		return nil, fmt.Errorf("wireguard: get tun interface name: %w", err)
	}

	logger := device.NewLogger(device.LogLevelError, "wireguard: ")
	dev := device.NewDevice(tunDev, wgconn.NewDefaultBind(), logger)

	if err := dev.IpcSet(uapiConfig(cfg)); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard: configure device: %w", err)
	}
	if err := configureLink(realName, cfg); err != nil {
		dev.Close()
		_ = removeLink(realName)
		return nil, fmt.Errorf("wireguard: configure interface: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		_ = removeLink(realName)
		return nil, fmt.Errorf("wireguard: bring device up: %w", err)
	}

	return &Tunnel{dev: dev, tunDev: tunDev, linkName: realName}, nil
}

// configureLink assigns cfg.Address, sets the MTU, brings the link up, and
// adds a wgRouteMetric-priority route for every peer's AllowedIPs -- all
// via netlink directly (no shelling out to ip/wg-quick, which aren't
// guaranteed present in a minimal container image).
func configureLink(name string, cfg *Config) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("find link %s: %w", name, err)
	}
	for _, addr := range cfg.Address {
		nlAddr := &netlink.Addr{IPNet: prefixToIPNet(addr)}
		if err := netlink.AddrAdd(link, nlAddr); err != nil {
			return fmt.Errorf("add address %s: %w", addr, err)
		}
	}
	if err := netlink.LinkSetMTU(link, cfg.MTU); err != nil {
		return fmt.Errorf("set mtu %d: %w", cfg.MTU, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("link up: %w", err)
	}
	for _, peer := range cfg.Peers {
		for _, allowed := range peer.AllowedIPs {
			route := &netlink.Route{
				LinkIndex: link.Attrs().Index,
				Dst:       prefixToIPNet(allowed),
				Priority:  wgRouteMetric,
			}
			if err := netlink.RouteAdd(route); err != nil && !errors.Is(err, unix.EEXIST) {
				return fmt.Errorf("add route for %s: %w", allowed, err)
			}
		}
	}
	return nil
}

// removeLink deletes the interface if it still exists; used for best-effort
// cleanup on both Start failure and Close. Never returns an error the
// caller needs to act on.
func removeLink(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil
	}
	return netlink.LinkDel(link)
}

func prefixToIPNet(p netip.Prefix) *net.IPNet {
	return &net.IPNet{
		IP:   p.Addr().AsSlice(),
		Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen()),
	}
}

// Close tears down the device and its kernel interface, releasing every
// goroutine/socket/fd it owns. Safe to call once; the manager guarantees a
// Tunnel is closed exactly once.
func (t *Tunnel) Close() {
	if t == nil || t.dev == nil {
		return
	}
	t.dev.Close()
	_ = removeLink(t.linkName)
}

// DialContext satisfies privacy.ContextDialer. It dials via the ordinary
// Go network stack (DNS resolution, if any, happens unbound/normally) but
// binds the resulting socket to this tunnel's kernel interface with
// SO_BINDTODEVICE before connecting -- forcing that specific connection's
// egress and ingress through the WireGuard interface regardless of the
// system's normal routing table, without altering the default route or
// affecting any other socket in the process (Plex/DB/local/other Drakkar
// traffic is untouched). There is no fallback path: if the interface is
// down or unreachable, the dial fails outright.
func (t *Tunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d := &net.Dialer{
		Control: func(_, _ string, c syscall.RawConn) error {
			var sockErr error
			if err := c.Control(func(fd uintptr) {
				sockErr = unix.BindToDevice(int(fd), t.linkName)
			}); err != nil {
				return err
			}
			return sockErr
		},
	}
	return d.DialContext(ctx, network, address)
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
