package nntp

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
)

// ContextDialer is the shape ArticleClient dials through -- satisfied
// directly by *net.Dialer-like adapters and by *privacy.Manager, so
// injecting privacy routing (SOCKS5/WireGuard) needs no adapter layer.
type ContextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type netDialer struct{ timeout time.Duration }

func (d netDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: d.timeout}
	return dialer.DialContext(ctx, network, address)
}

// ArticleClient is a raw NNTP client for a single Usenet provider account.
//
// Each call to NewSession dials a fresh connection, performs the greeting and
// AUTHINFO handshake, and returns a BodySession scoped to that connection --
// ArticleClient itself holds no connection state and is safe for concurrent
// use. Connection pooling and scheduling are layered on top by PooledSource
// and ScheduledSource; ArticleClient only knows how to establish one session.
type ArticleClient struct {
	provider config.UsenetProvider
	timeout  time.Duration
	dialer   ContextDialer
}

// NewArticleClient builds a client for provider. dialer selects the route
// (Direct/SOCKS5/WireGuard) every TCP/TLS connection uses; pass nil to fall
// back to a plain net.Dialer (existing behavior, used by tests).
func NewArticleClient(provider config.UsenetProvider, dialer ContextDialer) *ArticleClient {
	c := &ArticleClient{
		provider: provider,
		timeout:  30 * time.Second,
		dialer:   dialer,
	}
	if c.dialer == nil {
		c.dialer = netDialer{timeout: c.timeout}
	}
	return c
}

// Name returns the provider identifier used in logging and circuit-breaker
// state keys.
func (c *ArticleClient) Name() string {
	return "usenet:" + c.provider.Name
}

// Probe verifies the provider is reachable and credentials are accepted by
// opening and immediately closing a session. Used for connectivity/health
// checks rather than article retrieval.
func (c *ArticleClient) Probe(ctx context.Context) error {
	session, err := c.NewSession(ctx)
	if err != nil {
		return err
	}
	return session.Close()
}

// Body dials a new connection, fetches the article body, and closes the
// connection. Callers doing repeated fetches should use a PooledSource
// instead -- this path pays for a full TCP/TLS handshake per call.
func (c *ArticleClient) Body(ctx context.Context, messageID string) ([]byte, error) {
	session, err := c.NewSession(ctx)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	return session.Body(ctx, messageID)
}

// Stat dials a new connection and checks article existence. See Body for the
// per-call connection cost this incurs.
func (c *ArticleClient) Stat(ctx context.Context, messageID string) error {
	session, err := c.NewSession(ctx)
	if err != nil {
		return err
	}
	defer session.Close()
	return session.Stat(ctx, messageID)
}

// NewSession dials the provider, performs the greeting and (if credentials
// are configured) the AUTHINFO USER/PASS handshake, and returns a session
// ready for BODY/STAT commands.
//
// A deadline covering the full handshake is set on the connection because
// tls.DialWithDialer/HandshakeContext does not itself bound the plaintext
// greeting and AUTHINFO reads that follow; the deadline is cleared before
// returning so per-command deadlines (set in Body/Stat) take over.
func (c *ArticleClient) NewSession(ctx context.Context) (BodySession, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	// Set a deadline covering the entire greeting + auth handshake.
	// tls.DialWithDialer does not accept a context, so without this the
	// greeting/auth reads can block indefinitely if the server is slow.
	if err := conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		conn.Close()
		return nil, err
	}
	session := &clientSession{
		conn:    conn,
		reader:  bufio.NewReader(conn),
		writer:  bufio.NewWriter(conn),
		timeout: c.timeout,
	}
	if _, _, err := readStatusLine(session.reader); err != nil {
		conn.Close()
		return nil, err
	}
	if c.provider.Username != "" {
		if err := writeCommand(session.writer, "AUTHINFO USER "+c.provider.Username); err != nil {
			conn.Close()
			return nil, err
		}
		code, _, err := readStatusLine(session.reader)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if code == 381 {
			if err := writeCommand(session.writer, "AUTHINFO PASS "+c.provider.Password); err != nil {
				conn.Close()
				return nil, err
			}
			var passCode int
			passCode, _, err = readStatusLine(session.reader)
			if err != nil {
				conn.Close()
				return nil, err
			}
			if passCode != 281 {
				conn.Close()
				return nil, fmt.Errorf("NNTP authentication failed (code %d)", passCode)
			}
		}
	}
	// Clear the handshake deadline — per-command deadlines are set in Body/Stat.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return session, nil
}

// clientSession is the BodySession returned by ArticleClient.NewSession. It
// wraps a single dialed connection and is not safe for concurrent use --
// callers must serialize BODY/STAT commands on a given session (the pool
// layer enforces this by handing out one session per acquire()).
type clientSession struct {
	conn    net.Conn
	reader  *bufio.Reader
	writer  *bufio.Writer
	timeout time.Duration
}

func (s *clientSession) Body(ctx context.Context, messageID string) ([]byte, error) {
	deadline := time.Now().Add(s.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := s.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	// Expire the connection immediately when ctx is canceled so the pool slot
	// is freed before the 30s deadline — critical for responsive seek on movies
	// where a seek cancels up to 40 in-flight read-ahead connections.
	stop := context.AfterFunc(ctx, func() { _ = s.conn.SetDeadline(time.Now()) })
	defer stop()
	normalized := normalizeMessageID(messageID)
	if err := writeCommand(s.writer, "BODY "+normalized); err != nil {
		return nil, err
	}
	code, text, err := readStatusLine(s.reader)
	if err != nil {
		return nil, err
	}
	if code != 222 {
		return nil, fmt.Errorf("unexpected BODY status %d", code)
	}
	// The 222 response echoes "<article-number> <message-id>" (RFC 3977
	// §6.2.3) -- confirmed live against this project's actual provider
	// (Newshosting): every real BODY response reliably includes it. Requiring
	// it match before trusting the body that follows is the fix for a real,
	// confirmed-live 2026-08-23 production incident: a session whose protocol
	// stream had desynced (root cause not fully pinned down -- a provider-side
	// hiccup and a client-side race were both live candidates, but this check
	// is correct regardless of which) kept returning some OTHER article's
	// bytes for whatever messageID was actually requested, with no
	// transport-level error at all -- FetchRangeInfoPriority's yEnc-position
	// sanity check (see fetcher.go) could catch the resulting mismatch after
	// the fact, but by then this session had already been handed back to
	// PooledSource as "healthy" and reused for every subsequent caller,
	// repeating the same wrong answer for hundreds of unrelated requests
	// before whatever connection eventually got swept. Rejecting here, before
	// readMultilineBody, means PooledSource.discard closes this connection
	// immediately instead of recycling a desynced one back into the pool.
	if !strings.Contains(text, normalized) {
		return nil, fmt.Errorf("nntp: BODY response for %s did not echo the requested message-id (got %q) -- connection desynced", normalized, text)
	}
	return readMultilineBody(s.reader)
}

func (s *clientSession) Stat(ctx context.Context, messageID string) error {
	deadline := time.Now().Add(s.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := s.conn.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = s.conn.SetDeadline(time.Now()) })
	defer stop()
	normalized := normalizeMessageID(messageID)
	if err := writeCommand(s.writer, "STAT "+normalized); err != nil {
		return err
	}
	code, text, err := readStatusLine(s.reader)
	if err != nil {
		return err
	}
	if code == 430 {
		return ErrArticleMissing
	}
	if code != 223 {
		return fmt.Errorf("unexpected STAT status %d", code)
	}
	// See the matching check in Body for why this matters: a desynced
	// connection giving a stale 223 response for a different article would
	// otherwise report success for entirely the wrong messageID.
	if !strings.Contains(text, normalized) {
		return fmt.Errorf("nntp: STAT response for %s did not echo the requested message-id (got %q) -- connection desynced", normalized, text)
	}
	return nil
}

func (s *clientSession) Close() error {
	return s.conn.Close()
}

// dial establishes the TCP connection and, for TLS-enabled providers, layers
// the TLS handshake on top through the same ContextDialer so the plaintext
// TCP connect and the TLS handshake both honor ctx cancellation and route
// through c.dialer identically (SOCKS5/WireGuard proxies see one connection,
// not a raw dial followed by an unrelated TLS wrap).
func (c *ArticleClient) dial(ctx context.Context) (net.Conn, error) {
	address := net.JoinHostPort(c.provider.Host, strconv.Itoa(c.provider.Port))
	if c.provider.TLS {
		conn, err := c.dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: c.provider.Host,
			MinVersion: tls.VersionTLS12,
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		return tlsConn, nil
	}
	return c.dialer.DialContext(ctx, "tcp", address)
}

func writeCommand(writer *bufio.Writer, command string) error {
	if _, err := writer.WriteString(command + "\r\n"); err != nil {
		return err
	}
	return writer.Flush()
}

func readStatusLine(reader *bufio.Reader) (int, string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return 0, "", err
	}
	line = strings.TrimSpace(line)
	if len(line) < 3 {
		return 0, "", errors.New("short nntp status line")
	}
	code, err := strconv.Atoi(line[:3])
	if err != nil {
		return 0, "", err
	}
	return code, strings.TrimSpace(line[3:]), nil
}

// readMultilineBody reads an NNTP multiline data block, stopping at the
// terminating "." line and reversing dot-stuffing (a leading ".." on a data
// line represents a literal "." per RFC 3977 §3.1.1, used so a line of actual
// article content can never be mistaken for the terminator).
func readMultilineBody(reader *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if line == ".\r\n" || line == ".\n" {
			break
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		out = append(out, []byte(line)...)
	}
	return out, nil
}

func normalizeMessageID(messageID string) string {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return messageID
	}
	if strings.HasPrefix(messageID, "<") && strings.HasSuffix(messageID, ">") {
		return messageID
	}
	return "<" + strings.Trim(messageID, "<>") + ">"
}
