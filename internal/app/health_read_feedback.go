package app

import (
	"context"
	"sync"
	"time"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/library"
	"github.com/drakkar-media/drakkar/internal/workflow"
	"github.com/rs/zerolog"
)

// readFeedbackCooldown is how long one file is left alone after a re-check was
// scheduled for it. A player retries a missing article many times per second,
// so without this a single bad file would queue thousands of checks.
const readFeedbackCooldown = 30 * time.Minute

// readFailureRechecker turns real playback failures (an article the provider
// no longer has) into an immediate full-file re-check of that one file. The
// periodic deep check only samples first/last articles and runs on an
// age-scaled schedule, so before this a hole mid-file kept playing "healthy"
// until the player stalled at that offset, over and over.
type readFailureRechecker struct {
	db             *database.DB
	workflowSvc    *workflow.Service
	publicationSvc *library.Publisher
	logger         zerolog.Logger
	baseCtx        context.Context

	mu       sync.Mutex
	lastSeen map[int64]time.Time
	sem      chan struct{}
}

func newReadFailureRechecker(ctx context.Context, db *database.DB, workflowSvc *workflow.Service, publicationSvc *library.Publisher, logger zerolog.Logger) *readFailureRechecker {
	return &readFailureRechecker{
		db: db, workflowSvc: workflowSvc, publicationSvc: publicationSvc, logger: logger, baseCtx: ctx,
		lastSeen: make(map[int64]time.Time),
		sem:      make(chan struct{}, 2),
	}
}

// Report is the dav read-failure hook. It never blocks the read path.
func (r *readFailureRechecker) Report(virtualFileID int64, _ error) {
	now := time.Now()
	r.mu.Lock()
	if t, ok := r.lastSeen[virtualFileID]; ok && now.Sub(t) < readFeedbackCooldown {
		r.mu.Unlock()
		return
	}
	if len(r.lastSeen) > 1024 {
		for id, t := range r.lastSeen {
			if now.Sub(t) >= readFeedbackCooldown {
				delete(r.lastSeen, id)
			}
		}
	}
	r.lastSeen[virtualFileID] = now
	r.mu.Unlock()
	select {
	case r.sem <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-r.sem }()
		r.recheck(virtualFileID)
	}()
}

func (r *readFailureRechecker) recheck(virtualFileID int64) {
	ctx, cancel := context.WithTimeout(r.baseCtx, fullSegmentCheckTimeout+time.Minute)
	defer cancel()
	c, ok, err := r.db.GetDeepHealthCandidateForVirtualFile(ctx, virtualFileID)
	if err != nil || !ok || c.NZBDocumentID <= 0 {
		return
	}
	r.logger.Warn().Int64("libraryItemId", c.LibraryItemID).Str("title", c.Title).
		Msg("health check: playback hit a missing article — running full segment check now")
	checkErr := r.db.StrictCheckAllSegments(ctx, c.NZBDocumentID)
	if checkErr == nil || isTransientHealthCheckErr(checkErr) {
		return
	}
	failPublicationAndBlocklist(ctx, r.db, r.workflowSvc, r.logger, c, checkErr)
}
