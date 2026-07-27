package wireguard

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"regexp"
	"runtime"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
	wgconn "golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// genKeypair returns (privateKeyBase64, publicKeyBase64) for a valid
// Curve25519 WireGuard keypair -- ParseConfig/the real handshake both
// require the public key to actually be derived from the private scalar.
func genKeypair(t *testing.T) (privB64, pubB64 string) {
	t.Helper()
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		t.Fatal(err)
	}
	// Clamp per Curve25519/WireGuard convention.
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(priv[:]), base64.StdEncoding.EncodeToString(pub)
}

var listenPortRe = regexp.MustCompile(`(?m)^listen_port=(\d+)$`)

// canCreateRealTUN reports whether this process can create a real kernel
// TUN device (NET_ADMIN + /dev/net/tun) -- true on a normal Linux host or
// a privileged CI runner, false in a restricted/rootless environment.
// Tests that need Start() to actually bring up a real interface skip
// gracefully when this is false, rather than failing CI somewhere that
// can't support it.
func canCreateRealTUN(t *testing.T) bool {
	t.Helper()
	d, err := tun.CreateTUN("drakkarwgprobe", 1420)
	if err != nil {
		return false
	}
	d.Close()
	_ = removeLink("drakkarwgprobe")
	return true
}

// startFakePeer brings up a second, independent userspace WireGuard device
// (not going through this package's Config/Start) acting as the "VPN
// server" side of the tunnel, and serves a tiny TCP echo listener on it.
// Returns its UDP listen port and TCP echo port.
func startFakePeer(t *testing.T, privB64, peerPubB64 string, selfAddr, peerAddr netip.Addr) (udpPort, tcpPort int, closeFn func()) {
	t.Helper()
	privHex, err := keyToHex(privB64)
	if err != nil {
		t.Fatal(err)
	}
	peerPubHex, err := keyToHex(peerPubB64)
	if err != nil {
		t.Fatal(err)
	}

	tunDev, tnet, err := netstack.CreateNetTUN([]netip.Addr{selfAddr}, nil, 1420)
	if err != nil {
		t.Fatalf("create fake peer tun: %v", err)
	}
	dev := device.NewDevice(tunDev, wgconn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "fakepeer: "))
	uapi := fmt.Sprintf("private_key=%s\nlisten_port=0\npublic_key=%s\nallowed_ip=%s/32\n", privHex, peerPubHex, peerAddr.String())
	if err := dev.IpcSet(uapi); err != nil {
		t.Fatalf("fake peer IpcSet: %v", err)
	}
	if err := dev.Up(); err != nil {
		t.Fatalf("fake peer Up: %v", err)
	}

	got, err := dev.IpcGet()
	if err != nil {
		t.Fatalf("fake peer IpcGet: %v", err)
	}
	m := listenPortRe.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("could not find listen_port in IpcGet output: %s", got)
	}
	udpPort, _ = strconv.Atoi(m[1])

	ln, err := tnet.ListenTCP(&net.TCPAddr{IP: net.ParseIP(selfAddr.String()), Port: 8081})
	if err != nil {
		t.Fatalf("fake peer ListenTCP: %v", err)
	}
	tcpPort = 8081
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(c)
		}
	}()

	return udpPort, tcpPort, func() {
		ln.Close()
		dev.Close()
	}
}

// TestWireGuardTunnelTrafficTraversesRealHandshake proves the WireGuardDialer
// isn't a fake abstraction: it brings up two independent userspace WireGuard
// devices (a real X25519 handshake, real encrypted UDP between them) and
// confirms a TCP echo dialed through this package's Tunnel/DialContext
// actually reaches the "server" peer -- not the host network directly.
func TestWireGuardTunnelTrafficTraversesRealHandshake(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real WireGuard handshake test in -short mode")
	}
	if !canCreateRealTUN(t) {
		t.Skip("skipping: this environment cannot create a real TUN device (needs NET_ADMIN + /dev/net/tun)")
	}
	clientAddr := netip.MustParseAddr("10.19.0.2")
	serverAddr := netip.MustParseAddr("10.19.0.1")

	clientPriv, clientPub := genKeypair(t)
	serverPriv, serverPub := genKeypair(t)

	udpPort, tcpPort, closeServer := startFakePeer(t, serverPriv, clientPub, serverAddr, clientAddr)
	defer closeServer()

	confText := fmt.Sprintf(
		"[Interface]\nPrivateKey = %s\nAddress = %s/32\n\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32\nEndpoint = 127.0.0.1:%d\nPersistentKeepalive = 1\n",
		clientPriv, clientAddr.String(), serverPub, serverAddr.String(), udpPort,
	)

	cfg, err := ParseConfig(confText)
	if err != nil {
		t.Fatalf("parse client config: %v", err)
	}

	tunnel, err := Start(cfg)
	if err != nil {
		t.Fatalf("start client tunnel: %v", err)
	}
	defer tunnel.Close()

	target := net.JoinHostPort(serverAddr.String(), strconv.Itoa(tcpPort))

	var conn net.Conn
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		conn, err = tunnel.DialContext(ctx, "tcp", target)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial through wireguard tunnel never succeeded (handshake likely failed): %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer conn.Close()

	msg := []byte("hello-through-wireguard")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q", buf)
	}
}

// TestTunnelLifecycleRepeatedStartStop confirms repeated Start/Close cycles
// don't leak goroutines -- required since privacy.Manager.Reload rebuilds a
// tunnel on every WireGuard config change.
func TestTunnelLifecycleRepeatedStartStop(t *testing.T) {
	if !canCreateRealTUN(t) {
		t.Skip("skipping: this environment cannot create a real TUN device (needs NET_ADMIN + /dev/net/tun)")
	}
	priv, pub := genKeypair(t)
	_ = pub
	baseline := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		confText := fmt.Sprintf(
			"[Interface]\nPrivateKey = %s\nAddress = 10.20.0.2/32\n\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = 127.0.0.1:1\n",
			priv, pub,
		)
		cfg, err := ParseConfig(confText)
		if err != nil {
			t.Fatal(err)
		}
		tun, err := Start(cfg)
		if err != nil {
			t.Fatalf("start iteration %d: %v", i, err)
		}
		tun.Close()
	}
	time.Sleep(200 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > baseline+10 {
		t.Fatalf("goroutine leak suspected: baseline=%d after=%d", baseline, after)
	}
}

func TestKeyToHexRoundTrip(t *testing.T) {
	_, pub := genKeypair(t)
	hexKey, err := keyToHex(pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(hexKey) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(hexKey))
	}
	if _, err := hex.DecodeString(hexKey); err != nil {
		t.Fatalf("not valid hex: %v", err)
	}
}
