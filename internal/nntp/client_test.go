package nntp

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
)

type countingDialer struct {
	calls atomic.Int32
	fn    func(ctx context.Context, network, address string) (net.Conn, error)
}

func (d *countingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.calls.Add(1)
	return d.fn(ctx, network, address)
}

// TestArticleClientDialsThroughInjectedDialer confirms ArticleClient never
// builds its own net.Dialer when one is injected -- the mechanism the
// privacy.Manager (Direct/SOCKS5/WireGuard) relies on to intercept every
// NNTP connection.
func TestArticleClientDialsThroughInjectedDialer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("200 hello\r\n"))
			c.Close()
		}
	}()

	fake := &countingDialer{fn: func(ctx context.Context, network, address string) (net.Conn, error) {
		// Redirect to the fake listener regardless of the requested address --
		// proves the client used the injected dialer rather than dialing the
		// provider host itself (which wouldn't even resolve here).
		return (&net.Dialer{}).DialContext(ctx, network, ln.Addr().String())
	}}
	provider := config.UsenetProvider{Host: "unused.invalid", Port: 1}
	client := NewArticleClient(provider, fake)

	session, err := client.NewSession(context.Background())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer session.Close()

	if fake.calls.Load() != 1 {
		t.Fatalf("expected the injected dialer to be used exactly once, got %d calls", fake.calls.Load())
	}
}

// fakeSessionConn wires a clientSession to one end of an in-memory net.Pipe,
// running respond in a goroutine on the other end to play the server role:
// respond receives each line the client writes (already trimmed of \r\n) and
// returns the raw response line(s) to write back (already including \r\n).
// Returning "" sends nothing further (used once the fake server is done).
func fakeSessionConn(t *testing.T, respond func(cmd string) string) *clientSession {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { clientConn.Close() })
	go func() {
		defer serverConn.Close()
		serverReader := bufio.NewReader(serverConn)
		for {
			line, err := serverReader.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimRight(line, "\r\n")
			resp := respond(cmd)
			if resp == "" {
				return
			}
			if _, err := serverConn.Write([]byte(resp)); err != nil {
				return
			}
		}
	}()
	return &clientSession{
		conn:    clientConn,
		reader:  bufio.NewReader(clientConn),
		writer:  bufio.NewWriter(clientConn),
		timeout: 5 * time.Second,
	}
}

// TestClientSessionBodyAcceptsMatchingMessageIDEcho is the non-regression
// counterpart to the mismatch test below: a well-behaved 222 response that
// correctly echoes the requested message-id must still succeed and return
// the real body.
func TestClientSessionBodyAcceptsMatchingMessageIDEcho(t *testing.T) {
	session := fakeSessionConn(t, func(cmd string) string {
		if !strings.HasPrefix(cmd, "BODY ") {
			return ""
		}
		return "222 0 <real@id> body follows\r\nhello\r\n.\r\n"
	})
	body, err := session.Body(context.Background(), "real@id")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if string(body) != "hello\r\n" {
		t.Fatalf("got body %q", string(body))
	}
}

// TestClientSessionBodyRejectsMismatchedMessageIDEcho reproduces the live
// 2026-08-23 production incident: a connection whose protocol stream had
// desynced kept returning a DIFFERENT article's bytes under a 222 status that
// never actually confirmed which message-id it was for, no transport error
// anywhere -- and PooledSource, seeing Body return successfully, handed the
// same desynced connection back to the pool for the next caller to hit the
// exact same wrong answer. The 222 response's echoed message-id must be
// checked and a mismatch must fail loudly (verified live against the real
// provider: RFC-compliant servers always echo it), so PooledSource discards
// the connection instead of recycling it.
func TestClientSessionBodyRejectsMismatchedMessageIDEcho(t *testing.T) {
	session := fakeSessionConn(t, func(cmd string) string {
		if !strings.HasPrefix(cmd, "BODY ") {
			return ""
		}
		// Server claims success but the echoed message-id is for a
		// completely different article than what was requested.
		return "222 0 <some-other-article@elsewhere> body follows\r\nwrong data\r\n.\r\n"
	})
	_, err := session.Body(context.Background(), "real@id")
	if err == nil {
		t.Fatal("expected an error for a mismatched message-id echo, got nil")
	}
}

// TestClientSessionStatAcceptsMatchingMessageIDEcho is the non-regression
// counterpart to the Stat mismatch test below.
func TestClientSessionStatAcceptsMatchingMessageIDEcho(t *testing.T) {
	session := fakeSessionConn(t, func(cmd string) string {
		if !strings.HasPrefix(cmd, "STAT ") {
			return ""
		}
		return "223 0 <real@id> article exists\r\n"
	})
	if err := session.Stat(context.Background(), "real@id"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

// TestClientSessionStatRejectsMismatchedMessageIDEcho mirrors the Body
// mismatch test for STAT: a 223 that doesn't echo the requested message-id
// must not be trusted as confirming that article's existence.
func TestClientSessionStatRejectsMismatchedMessageIDEcho(t *testing.T) {
	session := fakeSessionConn(t, func(cmd string) string {
		if !strings.HasPrefix(cmd, "STAT ") {
			return ""
		}
		return "223 0 <some-other-article@elsewhere> article exists\r\n"
	})
	err := session.Stat(context.Background(), "real@id")
	if err == nil {
		t.Fatal("expected an error for a mismatched message-id echo, got nil")
	}
}

func TestReadStatusLine(t *testing.T) {
	code, text, err := readStatusLine(bufio.NewReader(strings.NewReader("222 0 <msg> body follows\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if code != 222 || text != "0 <msg> body follows" {
		t.Fatalf("got %d %q", code, text)
	}
}

func TestReadMultilineBody(t *testing.T) {
	body, err := readMultilineBody(bufio.NewReader(strings.NewReader("line1\r\n..line2\r\n.\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "line1\r\n.line2\r\n" {
		t.Fatalf("got %q", string(body))
	}
}

func TestNormalizeMessageID(t *testing.T) {
	tests := map[string]string{
		"msg@example":         "<msg@example>",
		"<msg@example>":       "<msg@example>",
		" <msg@example> \r\n": "<msg@example>",
	}
	for input, want := range tests {
		if got := normalizeMessageID(input); got != want {
			t.Fatalf("normalizeMessageID(%q) = %q want %q", input, got, want)
		}
	}
}
