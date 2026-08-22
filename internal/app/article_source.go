package app

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/nntp"
	"github.com/drakkar-media/drakkar/internal/privacy"
	"github.com/drakkar-media/drakkar/internal/stream"
	"github.com/rs/zerolog"
)

// errNoUsenetProviders is returned by every dynamicArticleSource fetch method
// when no chain has been built yet, or the currently active chain has no
// enabled providers -- e.g. before the first Rebuild call, or after a
// settings save that leaves every provider disabled.
var errNoUsenetProviders = errors.New("no enabled usenet providers configured")

// articleSourceChain is one complete, independently-closeable instance of
// the Usenet fetch pipeline: per-provider connection pools -> multi-provider
// fallback -> missing-article cache -> priority scheduler -> disk/RAM decoded
// caches -> the final SegmentFetcher every read ultimately goes through.
// Rebuilt wholesale whenever Usenet provider settings change; the previous
// chain is closed only after a new one is confirmed built successfully.
type articleSourceChain struct {
	fetcher                *nntp.SegmentFetcher
	pooled                 []*nntp.PooledSource
	scheduled              *nntp.ScheduledSource
	maxDownloadConnections int
	streamingPriorityPct   int
	evictCancel            context.CancelFunc
}

// Close stops every goroutine this chain owns (missing-article cache
// eviction, scheduler workers, per-provider pool sweep/keep-warm loops).
func (c *articleSourceChain) Close() {
	if c == nil {
		return
	}
	if c.evictCancel != nil {
		c.evictCancel()
	}
	if c.scheduled != nil {
		c.scheduled.Close()
	}
	for _, p := range c.pooled {
		p.Close()
	}
}

// splitForegroundWorkers divides maxDownloadConnections between the
// scheduler's interactive and read-ahead lanes using the same
// streamingPriorityPct that already governs ReadAheadManager's own
// per-session parallelism cap (SetConnectionBudget), so one setting
// consistently expresses "how much of the download budget favors
// speculative prefetch" at both layers. Confirmed live (2026-08-10, Venom:
// Let There Be Carnage): before this split, interactive and read-ahead
// shared one worker pool, so a read-ahead burst occupying every worker left
// a new interactive fetch nothing to preempt -- it could only wait for one
// to finish. Each side is floored at 1 so neither lane can ever end up with
// zero workers regardless of how extreme the percentage is configured.
func splitForegroundWorkers(maxDownloadConnections, streamingPriorityPct int) (interactiveWorkers, readAheadWorkers int) {
	pct := streamingPriorityPct
	if pct <= 0 || pct > 100 {
		pct = 80
	}
	readAheadWorkers = maxDownloadConnections * pct / 100
	interactiveWorkers = maxDownloadConnections - readAheadWorkers
	if interactiveWorkers < 1 {
		interactiveWorkers = 1
	}
	if readAheadWorkers < 1 {
		readAheadWorkers = 1
	}
	return interactiveWorkers, readAheadWorkers
}

// buildArticleSourceChain constructs the full Usenet fetch pipeline for the
// given provider list, exactly mirroring the pipeline built once at startup
// in Run -- extracted here so both Run and a live Usenet-settings reload
// build it identically.
func buildArticleSourceChain(ctx context.Context, rt config.Runtime, usenetCfg config.UsenetConfig, dialer nntp.ContextDialer, logger zerolog.Logger) *articleSourceChain {
	var (
		articleSources         []nntp.NamedArticleSource
		pooledSources          []*nntp.PooledSource
		totalWorkers           int
		maxDownloadConnections int
	)
	for _, provider := range usenetCfg.Providers {
		if !provider.Enabled || provider.Host == "" {
			continue
		}
		totalWorkers += max(provider.MaxConnections, 1)
	}
	if len(usenetCfg.Providers) > 0 {
		maxDownloadConnections = usenetCfg.MaxDownloadConnections
		if maxDownloadConnections <= 0 {
			maxDownloadConnections = totalWorkers
		}
		if maxDownloadConnections > totalWorkers {
			maxDownloadConnections = totalWorkers
		}
		if lacksSchedulerLaneHeadroom(totalWorkers, maxDownloadConnections) {
			logger.Warn().
				Int("maxDownloadConnections", maxDownloadConnections).
				Int("totalProviderConnections", totalWorkers).
				Msg("usenet: foreground + background NNTP lanes (2x maxDownloadConnections) have little or no headroom against the shared connection pool (sum of provider maxConnections) -- a background calibration/health-check burst could delay foreground playback fetches; consider raising provider maxConnections or lowering maxDownloadConnections")
		}
	}
	interactiveWorkers, readAheadWorkers := splitForegroundWorkers(maxDownloadConnections, usenetCfg.StreamingPriorityPct)
	for _, provider := range usenetCfg.Providers {
		if !provider.Enabled || provider.Host == "" {
			continue
		}
		client := nntp.NewArticleClient(provider, dialer)
		// Reserve this provider's proportional share of the interactive lane
		// so background/read-ahead traffic against THIS pool can never starve
		// an interactive fetch of a connection, regardless of how many
		// connections background work is holding elsewhere. See
		// PooledSource.acquireGate; mirrors nzbdav-rs's prioritized_semaphore
		// hard-reservation design (2026-08-11, Landman connection-pool
		// contention investigation).
		reserved := 0
		if maxDownloadConnections > 0 {
			reserved = provider.MaxConnections * interactiveWorkers / maxDownloadConnections
		}
		if reserved < 1 {
			reserved = 1
		}
		pooled := nntp.NewPooledSourceReserved(ctx, client.NewSession, provider.MaxConnections, reserved)
		pooledSources = append(pooledSources, pooled)
		articleSources = append(articleSources, nntp.NamedArticleSource{
			Name:   provider.Name,
			Source: pooled,
		})
	}
	if len(articleSources) == 0 {
		return &articleSourceChain{pooled: pooledSources}
	}

	fallback := nntp.NewFallbackSource(articleSources, 1)
	cachedFallback := nntp.NewCachedFallbackSource(fallback)
	scheduled := nntp.NewScheduledSourceLanes(ctx, cachedFallback, interactiveWorkers, readAheadWorkers, maxDownloadConnections, maxDownloadConnections*8)
	diskDecoded := nntp.NewDiskCachedDecodedSource(scheduled, rt.BlockCachePath, rt.DiskCacheLimitBytes)
	decoded := nntp.NewCachedDecodedSource(diskDecoded, rt.MemoryHotCacheMaxBytes)

	evictCtx, evictCancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-evictCtx.Done():
				return
			case <-ticker.C:
				cachedFallback.Evict()
			}
		}
	}()

	return &articleSourceChain{
		fetcher:                nntp.NewSegmentFetcher(decoded),
		pooled:                 pooledSources,
		scheduled:              scheduled,
		maxDownloadConnections: maxDownloadConnections,
		streamingPriorityPct:   usenetCfg.StreamingPriorityPct,
		evictCancel:            evictCancel,
	}
}

// dynamicArticleSource is db.SegmentFetcher's runtime type: a swap-pointer
// wrapper (same pattern as dynamicWorkQueue) so a Usenet provider settings
// change can rebuild the entire fetch pipeline and atomically swap it in
// without an application restart. Implements every interface the rest of
// the codebase type-asserts db.SegmentFetcher against (stream.SegmentFetcher,
// stream.PrioritySegmentFetcher, database.SegmentChecker, SegmentSizer, and
// the unexported rangeInfoFetcher) by delegating to whichever chain is
// currently active.
type dynamicArticleSource struct {
	rt     config.Runtime
	logger zerolog.Logger

	mu           sync.RWMutex
	chain        *articleSourceChain
	lastCfg      *config.UsenetConfig
	lastRouteCfg privacy.Config
}

func newDynamicArticleSource(rt config.Runtime, logger zerolog.Logger) *dynamicArticleSource {
	return &dynamicArticleSource{rt: rt, logger: logger}
}

// Rebuild constructs a fresh pipeline from usenetCfg/dialer and atomically
// swaps it in; the previous chain is closed only after the swap succeeds,
// so a rebuild can never leave zero working pipeline installed. A no-op if
// BOTH usenetCfg and the dialer's current route are identical to the last
// applied config -- ApplySettings calls this on every settings save
// regardless of what changed, and rebuilding (re-dialing every provider) on
// an unrelated save would needlessly churn live NNTP connections.
//
// The route comparison matters just as much as usenetCfg: dialer is always
// the same *privacy.Manager pointer (app.go wires it once at startup), so
// comparing it by identity can never detect a privacy-mode change on its
// own. Confirmed live 2026-08-22: a privacy routing switch (e.g. WireGuard
// to Direct) with no accompanying Usenet settings change in the same save
// left usenetCfg byte-identical, so this used to return here without
// rebuilding -- even though privacy.Manager.Reload (called just before this,
// in ApplySettings) had already torn down the old WireGuard tunnel every
// pooled connection in the untouched chain was still using. Every
// subsequent read failed instantly (write/read on an already-closed
// connection) regardless of which mode was now configured, since the stale
// pool was never replaced.
func (d *dynamicArticleSource) Rebuild(ctx context.Context, usenetCfg config.UsenetConfig, dialer nntp.ContextDialer) {
	routeCfg := currentRouteConfig(dialer)
	d.mu.RLock()
	unchanged := d.lastCfg != nil && reflect.DeepEqual(*d.lastCfg, usenetCfg) && d.lastRouteCfg == routeCfg
	d.mu.RUnlock()
	if unchanged {
		return
	}

	newChain := buildArticleSourceChain(ctx, d.rt, usenetCfg, dialer, d.logger)

	d.mu.Lock()
	old := d.chain
	d.chain = newChain
	cfgCopy := usenetCfg
	d.lastCfg = &cfgCopy
	d.lastRouteCfg = routeCfg
	d.mu.Unlock()

	if old != nil {
		go old.Close()
	}
}

// currentRouteConfig extracts dialer's current routing config for change
// detection, if it exposes one (true for the production *privacy.Manager
// dialer). Returns the zero Config for any other dialer (e.g. a test
// double), so callers that don't route through privacy.Manager see this
// term of the comparison as always-equal -- unchanged from Rebuild's
// pre-existing behavior for them.
func currentRouteConfig(dialer nntp.ContextDialer) privacy.Config {
	mgr, ok := dialer.(*privacy.Manager)
	if !ok {
		return privacy.Config{}
	}
	return mgr.CurrentConfig()
}

func (d *dynamicArticleSource) get() *articleSourceChain {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.chain
}

// Pools/Scheduled expose the current chain's components for metrics --
// always read fresh so a rebuild doesn't leave metrics reporting a stale,
// already-closed pool.
func (d *dynamicArticleSource) Pools() []*nntp.PooledSource {
	c := d.get()
	if c == nil {
		return nil
	}
	return c.pooled
}

func (d *dynamicArticleSource) Scheduled() *nntp.ScheduledSource {
	c := d.get()
	if c == nil {
		return nil
	}
	return c.scheduled
}

// ConnectionBudget returns the current chain's foreground connection budget
// and streaming priority percentage, for wiring into ReadAheadManager.
func (d *dynamicArticleSource) ConnectionBudget() (maxDownloadConnections, streamingPriorityPct int) {
	c := d.get()
	if c == nil {
		return 0, 0
	}
	return c.maxDownloadConnections, c.streamingPriorityPct
}

func (d *dynamicArticleSource) FetchRange(ctx context.Context, segment stream.SegmentRange) ([]byte, error) {
	c := d.get()
	if c == nil || c.fetcher == nil {
		return nil, errNoUsenetProviders
	}
	return c.fetcher.FetchRange(ctx, segment)
}

func (d *dynamicArticleSource) FetchRangePriority(ctx context.Context, segment stream.SegmentRange, priority stream.FetchPriority) ([]byte, error) {
	c := d.get()
	if c == nil || c.fetcher == nil {
		return nil, errNoUsenetProviders
	}
	return c.fetcher.FetchRangePriority(ctx, segment, priority)
}

func (d *dynamicArticleSource) FetchRangeInfo(ctx context.Context, segment stream.SegmentRange) ([]byte, stream.SegmentSpan, error) {
	c := d.get()
	if c == nil || c.fetcher == nil {
		return nil, stream.SegmentSpan{}, errNoUsenetProviders
	}
	return c.fetcher.FetchRangeInfo(ctx, segment)
}

func (d *dynamicArticleSource) Exists(ctx context.Context, messageID string) error {
	c := d.get()
	if c == nil || c.fetcher == nil {
		return errNoUsenetProviders
	}
	return c.fetcher.Exists(ctx, messageID)
}

func (d *dynamicArticleSource) DecodedSize(ctx context.Context, messageID string) (int64, error) {
	c := d.get()
	if c == nil || c.fetcher == nil {
		return 0, errNoUsenetProviders
	}
	return c.fetcher.DecodedSize(ctx, messageID)
}
