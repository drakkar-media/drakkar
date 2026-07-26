package nntp

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/drakkar-media/drakkar/internal/observability"
)

// ErrArticleMissing is returned by Stat on a 430 status. Note that some
// providers (including this one) also return 430 for a transient
// connection/transfer-limit throttle, not just a genuinely absent article —
// callers must not treat this as a definitive permanent-failure signal.
var ErrArticleMissing = errors.New("article missing")

type BodySession interface {
	Body(ctx context.Context, messageID string) ([]byte, error)
	// Stat checks article existence without downloading the body.
	// Returns ErrArticleMissing when the server responds 430.
	Stat(ctx context.Context, messageID string) error
	Close() error
}

type SessionFactory func(ctx context.Context) (BodySession, error)

// idleTimeout: close NNTP connections that have been idle for 30 seconds.
// This frees server-side resources when no playback is active and the
// background queue is quiet.
const idleTimeout = 30 * time.Second

// minWarmConns is how many connections keepWarm proactively redials in the
// background as soon as the sweep closes them, instead of leaving the pool
// at zero until the next real request pays for a fresh TCP+TLS handshake
// synchronously. Confirmed live (2026-07-25) as the dominant cause of "video
// takes a while to start again after a pause": any pause over idleTimeout
// (very easy to hit -- answering the door, a bathroom break) closed every
// connection, so resuming playback was never actually a warm resume, it was
// a full cold start every time. Deliberately small and independent of
// maxOpen/provider.MaxConnections -- this isn't about download throughput
// (that's still governed by the scheduler's foreground lane), just about
// having a couple of connections already sitting ready so the *first* read
// after a pause doesn't have to wait on a handshake. Keeping only a couple
// idle, instead of raising idleTimeout itself, preserves the original
// provider-friendly intent of the 30s sweep for the bulk of connections.
const minWarmConns = 2

// keepWarmInterval matches the sweep cadence so a connection closed by one
// sweep tick is typically replaced before the next.
const keepWarmInterval = idleTimeout / 2

type pooledSession struct {
	session   BodySession
	idleSince time.Time
}

type PooledSource struct {
	factory SessionFactory
	maxOpen int

	mu   sync.Mutex
	open int
	idle chan pooledSession
	// freed wakes a goroutine parked in acquire's wait-select when a slot is
	// closed out (discard, or a stale idle session reaped) rather than
	// handed off via idle -- acquire otherwise only wakes on idle or
	// ctx.Done(), so that capacity would go unnoticed until happenstance
	// (some other release) drained the select. Without this, a discarded
	// connection can leave an already-parked waiter blocked forever on a
	// request context that has no deadline (e.g. a FUSE/WebDAV read).
	freed chan struct{}
}

func NewPooledSource(ctx context.Context, factory SessionFactory, maxOpen int) *PooledSource {
	if maxOpen <= 0 {
		maxOpen = 1
	}
	p := &PooledSource{
		factory: factory,
		maxOpen: maxOpen,
		// Buffer beyond maxOpen: sweepOnce drains the channel into a local
		// slice before deciding what to keep, which briefly frees slots that
		// concurrent release() calls can fill; without slack, pushing the
		// kept (non-stale) sessions back can spuriously overflow and close
		// perfectly healthy connections. See sweepOnce.
		idle:  make(chan pooledSession, maxOpen*2),
		freed: make(chan struct{}, maxOpen),
	}
	go p.sweepLoop(ctx)
	go p.keepWarmLoop(ctx)
	return p
}

// keepWarmLoop proactively redials up to minWarmConns connections whenever
// the pool has fewer than that open, so a real request never has to pay for
// the handshake itself. Runs independently of sweepLoop (though on a
// matching cadence) and ticks once immediately on startup rather than
// waiting a full interval, so a freshly-started process has warm connections
// ready before the first request rather than after keepWarmInterval elapses.
func (p *PooledSource) keepWarmLoop(ctx context.Context) {
	warm := minWarmConns
	if warm > p.maxOpen {
		warm = p.maxOpen
	}
	if warm <= 0 {
		return
	}
	p.keepWarmOnceProtected(ctx, warm)
	ticker := time.NewTicker(keepWarmInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.keepWarmOnceProtected(ctx, warm)
		}
	}
}

func (p *PooledSource) keepWarmOnceProtected(ctx context.Context, warm int) {
	defer observability.Recover("nntp-pool-keepwarm")
	p.keepWarmOnce(ctx, warm)
}

// keepWarmOnce reserves a slot per missing warm connection up front (under
// p.mu, mirroring acquire's own accounting) before dialing, so a burst of
// real acquire() calls racing this can't be starved past maxOpen, and a
// dial failure cleanly gives the slot back.
func (p *PooledSource) keepWarmOnce(ctx context.Context, warm int) {
	for {
		p.mu.Lock()
		if p.open >= warm || p.open >= p.maxOpen {
			p.mu.Unlock()
			return
		}
		p.open++
		p.mu.Unlock()

		session, err := p.factory(ctx)
		if err != nil {
			p.mu.Lock()
			p.open--
			p.mu.Unlock()
			// Provider unreachable or refusing connections -- don't spin;
			// the next keepWarmInterval tick will retry.
			return
		}
		p.release(session)
	}
}

// notifyFreed wakes a parked acquire() waiter after p.open is decremented
// without a session to hand off. Non-blocking: if no one is waiting (or the
// buffer is momentarily full), the signal is simply not needed right now --
// acquire() re-checks p.open < p.maxOpen every time it loops.
func (p *PooledSource) notifyFreed() {
	select {
	case p.freed <- struct{}{}:
	default:
	}
}

// sweepLoop closes connections idle longer than idleTimeout, on a period of
// idleTimeout/2. Exits when ctx is cancelled (process shutdown) instead of
// running forever, and recovers a
// panic from each individual sweep so one bad tick can't silently end the
// loop and leak idle connections for the rest of the process lifetime.
func (p *PooledSource) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(idleTimeout / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sweepOnceProtected()
		}
	}
}

func (p *PooledSource) sweepOnceProtected() {
	defer observability.Recover("nntp-pool-sweep")
	p.sweepOnce()
}

func (p *PooledSource) sweepOnce() {
	cutoff := time.Now().Add(-idleTimeout)
	var keep []pooledSession
	for {
		select {
		case s := <-p.idle:
			if s.idleSince.Before(cutoff) {
				_ = s.session.Close()
				p.mu.Lock()
				p.open--
				p.mu.Unlock()
				p.notifyFreed()
			} else {
				keep = append(keep, s)
			}
		default:
			goto done
		}
	}
done:
	for _, s := range keep {
		select {
		case p.idle <- s:
		default:
			_ = s.session.Close()
			p.mu.Lock()
			p.open--
			p.mu.Unlock()
			p.notifyFreed()
		}
	}
}

func (p *PooledSource) Body(ctx context.Context, messageID string) ([]byte, error) {
	if p == nil || p.factory == nil {
		return nil, errors.New("pooled source unavailable")
	}
	session, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	body, err := session.Body(ctx, messageID)
	if err != nil {
		p.discard(session)
		return nil, err
	}
	p.release(session)
	return body, nil
}

func (p *PooledSource) Stat(ctx context.Context, messageID string) error {
	if p == nil || p.factory == nil {
		return errors.New("pooled source unavailable")
	}
	session, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	err = session.Stat(ctx, messageID)
	if err != nil {
		// ErrArticleMissing (430) means the article doesn't exist but the
		// connection itself is still valid — release rather than discard so we
		// don't create a new TCP+TLS handshake for every missing segment.
		if errors.Is(err, ErrArticleMissing) {
			p.release(session)
		} else {
			p.discard(session)
		}
		return err
	}
	p.release(session)
	return nil
}

func (p *PooledSource) acquire(ctx context.Context) (BodySession, error) {
	// Check ctx before borrowing — cancelled read-ahead must not steal
	// a pooled session from an interactive reader.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for {
		// Drain stale idle sessions, take first fresh one.
		for {
			select {
			case s := <-p.idle:
				if time.Since(s.idleSince) > idleTimeout {
					_ = s.session.Close()
					p.mu.Lock()
					p.open--
					p.mu.Unlock()
					p.notifyFreed()
					continue
				}
				return s.session, nil
			default:
				goto noIdle
			}
		}
	noIdle:
		p.mu.Lock()
		if p.open < p.maxOpen {
			p.open++
			p.mu.Unlock()
			session, err := p.factory(ctx)
			if err != nil {
				p.mu.Lock()
				p.open--
				p.mu.Unlock()
				return nil, err
			}
			return session, nil
		}
		p.mu.Unlock()

		// All connections in use — wait for one to be returned.
		select {
		case s := <-p.idle:
			if time.Since(s.idleSince) > idleTimeout {
				_ = s.session.Close()
				p.mu.Lock()
				p.open--
				p.mu.Unlock()
				p.notifyFreed()
				// loop back: open slot freed, retry immediately
			} else {
				return s.session, nil
			}
		case <-p.freed:
			// A slot closed out elsewhere (discard, or another waiter's
			// stale reap) without a session to hand off -- loop back and
			// recheck p.open < p.maxOpen to open a fresh connection.
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (p *PooledSource) release(session BodySession) {
	select {
	case p.idle <- pooledSession{session: session, idleSince: time.Now()}:
	default:
		p.discard(session)
	}
}

func (p *PooledSource) discard(session BodySession) {
	_ = session.Close()
	p.mu.Lock()
	p.open--
	p.mu.Unlock()
	p.notifyFreed()
}

// Stats returns current active and idle connection counts.
func (p *PooledSource) Stats() (active, idle int) {
	p.mu.Lock()
	active = p.open
	idle = len(p.idle)
	p.mu.Unlock()
	return
}
