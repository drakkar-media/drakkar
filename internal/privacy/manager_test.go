package privacy

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerDefaultsToDirect(t *testing.T) {
	m := NewManager()
	if m.Mode() != ModeDirect {
		t.Fatalf("expected default mode direct, got %s", m.Mode())
	}
	status := m.Status()
	if status.State != "direct" {
		t.Fatalf("expected status direct, got %s", status.State)
	}
}

func TestManagerReloadSOCKS5AndDialsThroughIt(t *testing.T) {
	proxy := startTestSOCKS5Server(t, "", "")
	host, portStr, _ := net.SplitHostPort(proxy.addr())
	var port int
	fmtSscan(portStr, &port)

	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()

	m := NewManager()
	err = m.Reload(context.Background(), Config{
		Mode:   ModeSOCKS5,
		SOCKS5: SOCKS5Config{Host: host, Port: port, TimeoutSeconds: 5},
	})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if m.Mode() != ModeSOCKS5 {
		t.Fatalf("expected socks5 mode active")
	}

	conn, err := m.DialContext(context.Background(), "tcp", echoLn.Addr().String())
	if err != nil {
		t.Fatalf("dial via manager: %v", err)
	}
	conn.Close()

	time.Sleep(50 * time.Millisecond)
	if accepted.Load() != 1 {
		t.Fatalf("expected the dial to have gone through the proxy to the echo listener, accepted=%d", accepted.Load())
	}
}

func TestManagerReloadRejectsBadCandidateKeepsOldActive(t *testing.T) {
	proxy := startTestSOCKS5Server(t, "", "")
	host, portStr, _ := net.SplitHostPort(proxy.addr())
	var port int
	fmtSscan(portStr, &port)

	m := NewManager()
	if err := m.Reload(context.Background(), Config{
		Mode:   ModeSOCKS5,
		SOCKS5: SOCKS5Config{Host: host, Port: port, TimeoutSeconds: 5},
	}); err != nil {
		t.Fatalf("first reload: %v", err)
	}
	if m.Mode() != ModeSOCKS5 {
		t.Fatal("expected socks5 active after first reload")
	}

	// A candidate WireGuard config that fails to parse must not disturb the
	// currently active SOCKS5 route.
	err := m.Reload(context.Background(), Config{
		Mode:                ModeWireGuard,
		WireGuardConfigText: "not a valid wireguard config",
	})
	if err == nil {
		t.Fatal("expected reload to fail for invalid wireguard config")
	}
	if m.Mode() != ModeSOCKS5 {
		t.Fatalf("expected mode to remain socks5 after failed candidate, got %s", m.Mode())
	}
}

func TestManagerSOCKS5UnavailableNeverFallsBackToDirect(t *testing.T) {
	m := NewManager()
	if err := m.Reload(context.Background(), Config{
		Mode:   ModeSOCKS5,
		SOCKS5: SOCKS5Config{Host: "127.0.0.1", Port: 1, TimeoutSeconds: 1}, // nothing listening
	}); err != nil {
		t.Fatalf("reload should succeed (proxy config is structurally valid): %v", err)
	}

	// Dial some real, reachable direct target -- success here would mean a leak.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	_, err = m.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err == nil {
		t.Fatal("expected dial failure: socks5 proxy unreachable must not fall back direct")
	}
}

func TestManagerTransportRoutesThroughCurrentMode(t *testing.T) {
	m := NewManager()
	transport := m.Transport()

	var dialed atomic.Bool
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			dialed.Store(true)
			c.Close()
		}
	}()

	conn, err := transport.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("transport dial: %v", err)
	}
	conn.Close()
	time.Sleep(50 * time.Millisecond)
	if !dialed.Load() {
		t.Fatal("expected transport's DialContext to reach the direct listener")
	}
}

func TestManagerTestDoesNotMutateActiveState(t *testing.T) {
	m := NewManager()
	err := m.Test(context.Background(), Config{
		Mode:   ModeSOCKS5,
		SOCKS5: SOCKS5Config{Host: "127.0.0.1", Port: 1, TimeoutSeconds: 1},
	}, "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected Test to fail against an unreachable proxy")
	}
	if m.Mode() != ModeDirect {
		t.Fatalf("Test must never mutate active state, got mode %s", m.Mode())
	}
}
