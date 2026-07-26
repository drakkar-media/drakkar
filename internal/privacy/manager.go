package privacy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drakkar-media/drakkar/internal/privacy/wireguard"
)

// Config is the manager-level view of privacy settings, deliberately
// decoupled from internal/config so this package has no dependency on the
// rest of the app -- internal/app maps config.PrivacyConfig into this shape.
type Config struct {
	Mode                    Mode
	SOCKS5                  SOCKS5Config
	WireGuardConfigText     string
	WireGuardTimeoutSeconds int
}

// managerState is the immutable snapshot swapped atomically on Reload.
type managerState struct {
	mode     Mode
	dialer   ContextDialer
	wgTunnel *wireguard.Tunnel
	wgConfig *wireguard.Config
	initErr  error
}

// Manager is the single point every privacy-routed dial and HTTP client
// goes through. Direct/SOCKS5/WireGuard are mutually exclusive, atomically
// swapped states -- there is no code path that falls back to Direct when
// SOCKS5/WireGuard is selected but unavailable.
type Manager struct {
	state atomic.Pointer[managerState]

	mu         sync.Mutex
	transports []*http.Transport

	// drainFns are invoked after every successful Reload so pooled NNTP
	// connections dialed under the previous route don't linger indefinitely.
	drainFns []func()
}

// NewManager creates a Manager pre-initialized to ModeDirect so callers have
// a working dialer before the first Reload (e.g. during app startup, before
// configuration has been loaded from the database).
func NewManager() *Manager {
	m := &Manager{}
	m.state.Store(&managerState{mode: ModeDirect, dialer: NewDirectDialer(15 * time.Second)})
	return m
}

// OnDrain registers a callback invoked after each successful Reload, used
// to recycle connection pools (e.g. nntp.PooledSource.DrainIdle) that don't
// go through Manager.DialContext on every single call.
func (m *Manager) OnDrain(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drainFns = append(m.drainFns, fn)
}

// buildState constructs the managerState for cfg without touching any
// currently active state. On WireGuard/SOCKS5 initialization failure it
// returns a state carrying initErr (and, for WireGuard, the parsed cfg for
// diagnostics) rather than an error return, so both Reload and Test can
// inspect a candidate before deciding whether to activate it.
func buildState(cfg Config) *managerState {
	switch cfg.Mode {
	case ModeDirect, "":
		return &managerState{mode: ModeDirect, dialer: NewDirectDialer(15 * time.Second)}
	case ModeSOCKS5:
		d, err := NewSOCKS5Dialer(cfg.SOCKS5)
		if err != nil {
			return &managerState{mode: ModeSOCKS5, initErr: err}
		}
		return &managerState{mode: ModeSOCKS5, dialer: d}
	case ModeWireGuard:
		parsed, err := wireguard.ParseConfig(cfg.WireGuardConfigText)
		if err != nil {
			return &managerState{mode: ModeWireGuard, initErr: err}
		}
		tunnel, err := wireguard.Start(parsed)
		if err != nil {
			return &managerState{mode: ModeWireGuard, initErr: err, wgConfig: parsed}
		}
		return &managerState{mode: ModeWireGuard, dialer: tunnel, wgTunnel: tunnel, wgConfig: parsed}
	default:
		return &managerState{mode: cfg.Mode, initErr: fmt.Errorf("privacy: unknown mode %q", cfg.Mode)}
	}
}

// Reload builds a candidate route from cfg, and only swaps the active
// pointer if the candidate initialized successfully -- the previously
// working route stays active on failure (candidate/swap safety), and the
// old WireGuard tunnel (if any) is torn down only after the swap succeeds.
func (m *Manager) Reload(ctx context.Context, cfg Config) error {
	candidate := buildState(cfg)
	if candidate.initErr != nil {
		return candidate.initErr
	}

	old := m.state.Swap(candidate)

	m.mu.Lock()
	transports := append([]*http.Transport(nil), m.transports...)
	drains := append([]func(){}, m.drainFns...)
	m.mu.Unlock()
	for _, t := range transports {
		t.CloseIdleConnections()
	}
	for _, drain := range drains {
		drain()
	}

	if old != nil && old.wgTunnel != nil && old.wgTunnel != candidate.wgTunnel {
		old.wgTunnel.Close()
	}
	return nil
}

// DialContext is the single dial entry point for all protected traffic.
// The mode switch has exactly three cases and no default-to-direct branch,
// so a broken SOCKS5/WireGuard route fails the dial rather than leaking.
func (m *Manager) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	st := m.state.Load()
	if st.dialer == nil {
		return nil, fmt.Errorf("privacy: %s route unavailable: %w", st.mode, st.initErr)
	}
	return st.dialer.DialContext(ctx, network, address)
}

// Transport returns a new *http.Transport whose dials always go through the
// manager's current route. The manager tracks it so Reload can close its
// idle connections after a routing change -- new requests immediately use
// the new route regardless.
func (m *Manager) Transport() *http.Transport {
	t := &http.Transport{
		DialContext:           m.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	m.mu.Lock()
	m.transports = append(m.transports, t)
	m.mu.Unlock()
	return t
}

// HTTPClient is a convenience wrapper around Transport for callers that
// just want a ready-to-use privacy-routed *http.Client.
func (m *Manager) HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: m.Transport(), Timeout: timeout}
}

// Mode reports the currently active routing mode.
func (m *Manager) Mode() Mode {
	return m.state.Load().mode
}

// Status returns the read-only runtime view for the settings/status API.
func (m *Manager) Status() Status {
	st := m.state.Load()
	s := Status{Mode: st.mode, ProtectedTraffic: protectedTraffic}
	switch {
	case st.initErr != nil:
		s.State = "error"
		s.Error = st.initErr.Error()
	case st.mode == ModeDirect:
		s.State = "direct"
	case st.mode == ModeSOCKS5:
		s.State = "connected"
	case st.mode == ModeWireGuard:
		s.State = "connected"
	}
	if st.wgConfig != nil {
		summary := st.wgConfig.Summary()
		s.Endpoint = summary.Endpoint
		s.WireGuard = &WireGuardStatus{
			InterfaceAddress:    summary.InterfaceAddress,
			DNS:                 summary.DNS,
			Endpoint:            summary.Endpoint,
			AllowedIPs:          summary.AllowedIPs,
			PersistentKeepalive: summary.PersistentKeepalive,
		}
	}
	return s
}

// Test builds an ephemeral route from cfg (without activating it) and
// attempts to dial targetAddr, so the UI can validate a draft configuration
// before Save. Never falls back to Direct and never mutates active state.
func (m *Manager) Test(ctx context.Context, cfg Config, targetAddr string) error {
	candidate := buildState(cfg)
	if candidate.initErr != nil {
		return candidate.initErr
	}
	defer func() {
		if candidate.wgTunnel != nil {
			candidate.wgTunnel.Close()
		}
	}()
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := candidate.dialer.DialContext(dialCtx, "tcp", targetAddr)
	if err != nil {
		return err
	}
	return conn.Close()
}

// Close tears down the currently active route (WireGuard tunnel, if any).
// Used at process shutdown.
func (m *Manager) Close() {
	st := m.state.Load()
	if st.wgTunnel != nil {
		st.wgTunnel.Close()
	}
}
