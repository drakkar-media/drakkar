package privacy

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// testSOCKS5Server is a minimal RFC 1928/1929 SOCKS5 CONNECT server, used
// only to exercise SOCKS5Dialer end-to-end without a real external proxy.
type testSOCKS5Server struct {
	ln       net.Listener
	username string
	password string
}

func startTestSOCKS5Server(t *testing.T, username, password string) *testSOCKS5Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &testSOCKS5Server{ln: ln, username: username, password: password}
	go s.serve(t)
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *testSOCKS5Server) addr() string { return s.ln.Addr().String() }

func (s *testSOCKS5Server) serve(t *testing.T) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(t, conn)
	}
}

func (s *testSOCKS5Server) handle(t *testing.T, conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)

	// Greeting: VER NMETHODS METHODS...
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return
	}
	nmethods := int(hdr[1])
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(r, methods); err != nil {
		return
	}

	requireAuth := s.username != ""
	var chosen byte = 0x00 // no auth
	if requireAuth {
		chosen = 0x02 // user/pass
	}
	if _, err := conn.Write([]byte{0x05, chosen}); err != nil {
		return
	}

	if requireAuth {
		ahdr := make([]byte, 2)
		if _, err := io.ReadFull(r, ahdr); err != nil {
			return
		}
		ulen := int(ahdr[1])
		user := make([]byte, ulen)
		if _, err := io.ReadFull(r, user); err != nil {
			return
		}
		plenB := make([]byte, 1)
		if _, err := io.ReadFull(r, plenB); err != nil {
			return
		}
		pass := make([]byte, int(plenB[0]))
		if _, err := io.ReadFull(r, pass); err != nil {
			return
		}
		ok := string(user) == s.username && string(pass) == s.password
		status := byte(0x00)
		if !ok {
			status = 0x01
		}
		conn.Write([]byte{0x01, status})
		if !ok {
			return
		}
	}

	// Request: VER CMD RSV ATYP ADDR PORT
	req := make([]byte, 4)
	if _, err := io.ReadFull(r, req); err != nil {
		return
	}
	var targetHost string
	switch req[3] {
	case 0x01: // IPv4
		b := make([]byte, 4)
		io.ReadFull(r, b)
		targetHost = net.IP(b).String()
	case 0x03: // domain
		lb := make([]byte, 1)
		io.ReadFull(r, lb)
		b := make([]byte, int(lb[0]))
		io.ReadFull(r, b)
		targetHost = string(b)
	case 0x04: // IPv6
		b := make([]byte, 16)
		io.ReadFull(r, b)
		targetHost = net.IP(b).String()
	default:
		return
	}
	portB := make([]byte, 2)
	io.ReadFull(r, portB)
	port := binary.BigEndian.Uint16(portB)

	target, err := net.DialTimeout("tcp", net.JoinHostPort(targetHost, itoa(int(port))), 5*time.Second)
	if err != nil {
		conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer target.Close()

	reply := []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	if _, err := conn.Write(reply); err != nil {
		return
	}

	errCh := make(chan error, 2)
	go func() { _, err := io.Copy(target, r); errCh <- err }()
	go func() { _, err := io.Copy(conn, target); errCh <- err }()
	<-errCh
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestSOCKS5DialerConnectsThroughProxy(t *testing.T) {
	// Echo server that the SOCKS5 proxy will forward to.
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(c)
		}
	}()

	proxy := startTestSOCKS5Server(t, "", "")
	host, portStr, _ := net.SplitHostPort(proxy.addr())
	port := 0
	fmtSscan(portStr, &port)

	dialer, err := NewSOCKS5Dialer(SOCKS5Config{Host: host, Port: port, TimeoutSeconds: 5})
	if err != nil {
		t.Fatalf("build dialer: %v", err)
	}

	conn, err := dialer.DialContext(context.Background(), "tcp", echoLn.Addr().String())
	if err != nil {
		t.Fatalf("dial through socks5: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello-through-socks5")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q", buf)
	}
}

func TestSOCKS5DialerAuthentication(t *testing.T) {
	proxy := startTestSOCKS5Server(t, "user1", "pass1")
	host, portStr, _ := net.SplitHostPort(proxy.addr())
	port := 0
	fmtSscan(portStr, &port)

	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		c, err := echoLn.Accept()
		if err == nil {
			c.Close()
		}
	}()

	// Wrong credentials must fail.
	badDialer, err := NewSOCKS5Dialer(SOCKS5Config{Host: host, Port: port, Username: "user1", Password: "wrong", TimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badDialer.DialContext(context.Background(), "tcp", echoLn.Addr().String()); err == nil {
		t.Fatal("expected auth failure with wrong password")
	}

	goodDialer, err := NewSOCKS5Dialer(SOCKS5Config{Host: host, Port: port, Username: "user1", Password: "pass1", TimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := goodDialer.DialContext(context.Background(), "tcp", echoLn.Addr().String())
	if err != nil {
		t.Fatalf("expected success with correct credentials: %v", err)
	}
	conn.Close()
}

func TestSOCKS5DialerUnreachableProxyNeverFallsBackDirect(t *testing.T) {
	// Nothing listening on this port.
	unreachable := "127.0.0.1:1"
	host, portStr, _ := net.SplitHostPort(unreachable)
	port := 0
	fmtSscan(portStr, &port)

	dialer, err := NewSOCKS5Dialer(SOCKS5Config{Host: host, Port: port, TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Dial some real, reachable target -- if this ever succeeded it would
	// prove a fallback-to-direct leak. It must fail because the proxy itself
	// is unreachable.
	_, err = dialer.DialContext(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil {
		t.Fatal("expected failure: proxy unreachable, must not silently succeed via direct")
	}
}

func fmtSscan(s string, out *int) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			continue
		}
		n = n*10 + int(c-'0')
	}
	*out = n
}
