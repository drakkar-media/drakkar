package nntp

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"

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
