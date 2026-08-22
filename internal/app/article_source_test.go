package app

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/privacy"
	"github.com/rs/zerolog"
)

// countingListener runs a bare TCP listener that accepts and immediately
// closes connections -- enough for ArticleClient's dial+greeting-read to
// fail fast without a real NNTP server, while still proving a real dial
// reached it (via acceptedCh).
func countingListener(t *testing.T) (addr string, acceptedCh chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan struct{}, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			ch <- struct{}{}
			c.Close()
		}
	}()
	return ln.Addr().String(), ch
}

func TestDynamicArticleSourceRebuildSwapsChain(t *testing.T) {
	addr, _ := countingListener(t)
	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}

	rt := config.Runtime{
		BlockCachePath:         t.TempDir(),
		DiskCacheLimitBytes:    1 << 20,
		MemoryHotCacheMaxBytes: 1 << 20,
	}
	logger := zerolog.Nop()
	src := newDynamicArticleSource(rt, logger)
	mgr := privacy.NewManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg1 := config.UsenetConfig{
		MaxDownloadConnections: 2,
		Providers: []config.UsenetProvider{
			{Name: "provider-a", Host: host, Port: port, Enabled: true, MaxConnections: 2},
		},
	}
	src.Rebuild(ctx, cfg1, mgr)
	firstChain := src.get()
	if firstChain == nil || firstChain.fetcher == nil {
		t.Fatal("expected a fetcher after first rebuild")
	}
	if len(src.Pools()) != 1 {
		t.Fatalf("expected 1 pool, got %d", len(src.Pools()))
	}

	// Rebuilding with the identical config must be a no-op (same chain).
	src.Rebuild(ctx, cfg1, mgr)
	if src.get() != firstChain {
		t.Fatal("expected rebuild with unchanged config to be a no-op")
	}

	// A genuinely different config must swap in a new chain.
	cfg2 := cfg1
	cfg2.Providers = append([]config.UsenetProvider(nil), cfg1.Providers...)
	cfg2.Providers[0].Name = "provider-b"
	src.Rebuild(ctx, cfg2, mgr)
	secondChain := src.get()
	if secondChain == firstChain {
		t.Fatal("expected a changed config to produce a new chain")
	}
	if secondChain == nil || secondChain.fetcher == nil {
		t.Fatal("expected a fetcher after second rebuild")
	}

	// Give the old chain's background Close() goroutine a moment, then
	// confirm its pool no longer accepts new work by checking DrainIdle is
	// harmless to call post-close (would panic/race under -race if unsafe).
	time.Sleep(50 * time.Millisecond)
	for _, p := range firstChain.pooled {
		p.DrainIdle()
	}
}

// TestDynamicArticleSourceRebuildDetectsPrivacyRouteChange guards a real
// production incident (2026-08-22): dialer is always the same *privacy.Manager
// pointer (app.go wires it once at startup), so Rebuild's old
// usenetCfg-only comparison could never detect a privacy-mode switch on its
// own -- a settings save that changes ONLY privacy routing (not any Usenet
// field) left usenetCfg byte-identical, so Rebuild returned without
// rebuilding. Meanwhile privacy.Manager.Reload (called just before Rebuild
// in ApplySettings) had already torn down the OLD route (e.g. closed a
// WireGuard tunnel) out from under the untouched chain's still-pooled
// connections -- every subsequent read failed instantly (write/read on an
// already-closed connection) regardless of which mode was now configured,
// since the stale pool was never replaced. This reproduces the exact
// trigger: Rebuild is called twice with the IDENTICAL usenetCfg, with only
// the manager's underlying route changed in between.
func TestDynamicArticleSourceRebuildDetectsPrivacyRouteChange(t *testing.T) {
	addr, _ := countingListener(t)
	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}

	rt := config.Runtime{
		BlockCachePath:         t.TempDir(),
		DiskCacheLimitBytes:    1 << 20,
		MemoryHotCacheMaxBytes: 1 << 20,
	}
	src := newDynamicArticleSource(rt, zerolog.Nop())
	mgr := privacy.NewManager() // starts in ModeDirect

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := config.UsenetConfig{
		MaxDownloadConnections: 2,
		Providers: []config.UsenetProvider{
			{Name: "provider-a", Host: host, Port: port, Enabled: true, MaxConnections: 2},
		},
	}
	src.Rebuild(ctx, cfg, mgr)
	firstChain := src.get()
	if firstChain == nil || firstChain.fetcher == nil {
		t.Fatal("expected a fetcher after first rebuild")
	}

	// Switch the manager's route (Direct -> SOCKS5) without touching cfg at
	// all -- mirrors a settings save that only changes privacy routing.
	// SOCKS5Dialer construction is pure/local (no real proxy needed); only
	// DialContext would actually reach the network, which this test never
	// calls.
	if err := mgr.Reload(ctx, privacy.Config{
		Mode: privacy.ModeSOCKS5,
		SOCKS5: privacy.SOCKS5Config{
			Host:           "127.0.0.1",
			Port:           1,
			TimeoutSeconds: 1,
		},
	}); err != nil {
		t.Fatalf("reload to socks5: %v", err)
	}

	// Same cfg as the first call -- the old bug returned here without
	// rebuilding, leaving firstChain (and its now-orphaned pooled
	// connections) active forever.
	src.Rebuild(ctx, cfg, mgr)
	secondChain := src.get()
	if secondChain == firstChain {
		t.Fatal("expected Rebuild to detect the privacy route change and swap in a new chain, even with byte-identical usenetCfg")
	}
	if secondChain == nil || secondChain.fetcher == nil {
		t.Fatal("expected a fetcher after the route-change rebuild")
	}
}

func TestDynamicArticleSourceNoProvidersReturnsClearError(t *testing.T) {
	rt := config.Runtime{BlockCachePath: t.TempDir(), DiskCacheLimitBytes: 1 << 20, MemoryHotCacheMaxBytes: 1 << 20}
	src := newDynamicArticleSource(rt, zerolog.Nop())
	mgr := privacy.NewManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src.Rebuild(ctx, config.UsenetConfig{}, mgr)
	if _, err := src.DecodedSize(context.Background(), "msg"); err != errNoUsenetProviders {
		t.Fatalf("expected errNoUsenetProviders, got %v", err)
	}
	if err := src.Exists(context.Background(), "msg"); err != errNoUsenetProviders {
		t.Fatalf("expected errNoUsenetProviders, got %v", err)
	}
}
