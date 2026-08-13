package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/drakkar-media/drakkar/internal/nntp"
	"github.com/drakkar-media/drakkar/internal/observability"
	"github.com/drakkar-media/drakkar/internal/yenc"
)

// SegmentSizer can return the actual decoded byte size of an NNTP article.
type SegmentSizer interface {
	DecodedSize(ctx context.Context, messageID string) (int64, error)
}

// SegmentChecker confirms whether an NNTP article still exists via a cheap
// STAT-style check, without downloading its body.
type SegmentChecker interface {
	Exists(ctx context.Context, messageID string) error
}

type segmentPair struct{ first, last string }

// loadNZBFirstLastSegmentPairs returns a single representative pair — the
// first segment of the first qualifying file and the last segment of the
// last qualifying file — rather than one pair per file. Checking every
// volume of a multi-part RAR (routinely 90+ files) meant up to 8 concurrent
// NNTP checks per candidate, for every candidate a busy backlog cycle tried;
// that's the burst that was tripping the provider circuit breaker. A Usenet
// post propagates as a whole, so confirming its first and last piece are
// both reachable is a reasonable reachability signal without walking every
// file in between.
func (db *DB) loadNZBFirstLastSegmentPairs(ctx context.Context, nzbDocumentID int64) ([]segmentPair, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT
		    nf.subject,
		    nf.message_ids_packed,
		    coalesce(nf.message_ids::text, '{}')
		FROM nzb_files nf
		WHERE nf.nzb_document_id = $1
		  AND (
		    nf.message_id_count > 0
		    OR coalesce(array_length(nf.message_ids, 1), 0) > 0
		  )
		ORDER BY nf.id ASC`, nzbDocumentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var qualifying []segmentPair
	for rows.Next() {
		var subject string
		var packed []byte
		var raw string
		if err := rows.Scan(&subject, &packed, &raw); err != nil {
			return nil, err
		}
		if !shouldValidateNZBSubject(subject) {
			continue
		}
		ids, err := unpackMessageIDs(packed, raw)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			continue
		}
		p := segmentPair{first: ids[0], last: ids[len(ids)-1]}
		qualifying = append(qualifying, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(qualifying) == 0 {
		return nil, nil
	}
	return []segmentPair{{
		first: qualifying[0].first,
		last:  qualifying[len(qualifying)-1].last,
	}}, nil
}

// shouldValidateNZBSubject reports whether an NZB file's subject names a file
// worth a reachability check. Metadata/image files (.par2, .sfv, .nfo,
// images) and sample clips are excluded since their absence doesn't affect
// playability.
func shouldValidateNZBSubject(subject string) bool {
	name := parseNZBSubjectFilename(subject)
	if name == "" {
		name = subject
	}
	base := strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	if base == "" {
		return false
	}
	switch ext := strings.ToLower(filepath.Ext(base)); ext {
	case ".par2", ".sfv", ".nfo", ".jpg", ".jpeg", ".png":
		return false
	}
	return !isSampleFilename(base)
}

// parseNZBSubjectFilename extracts the filename from a Usenet subject line,
// preferring a quoted substring and falling back to the first
// whitespace-delimited field.
func parseNZBSubjectFilename(subject string) string {
	start := strings.Index(subject, "\"")
	end := strings.LastIndex(subject, "\"")
	if start >= 0 && end > start {
		return subject[start+1 : end]
	}
	fields := strings.Fields(subject)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], "\"")
}

// interiorSampleBudget bounds how many additional interior (neither first nor
// last) segments PreflightCheckFirstSegments samples for a single-file
// release, on top of the existing first/last check. Confirmed live
// (2026-08-11, Landman S01E01/E02/E04+): a release can have a perfectly
// reachable first and last segment while still having several articles
// missing somewhere in the middle -- Newshosting returned "430 No Such
// Article" for scattered segments of a specific poster's upload, so
// first/last-only reachability gave a false pass and the file only failed
// hours later, mid-playback, once a real read reached the missing offset.
// Deliberately NOT extended to multi-file (RAR volume) releases -- see
// loadNZBInteriorSampleSegments -- since checking every volume of a 90-file
// RAR set at this sample density is exactly the burst that originally
// tripped the provider circuit breaker (see loadNZBFirstLastSegmentPairs).
// This only runs once per already-selected candidate (not per search
// result), so the added check volume stays small even at this budget.
const interiorSampleBudget = 40

// PreflightCheckFirstSegments verifies that the first AND last segment of every
// NZB file in the given document is reachable on NNTP, plus a bounded sample
// of interior segments for a single-file release (see interiorSampleBudget).
// Unlike CalibrateNZBOffsets (which silently skips missing segments), this
// returns an error immediately so the workqueue can reject the release and
// fall back to the next candidate.
func (db *DB) PreflightCheckFirstSegments(ctx context.Context, nzbDocumentID int64) error {
	checker, ok := db.SegmentFetcher.(SegmentChecker)
	if !ok || checker == nil {
		return nil // NNTP fetcher not available; skip preflight
	}
	pairs, err := db.loadNZBFirstLastSegmentPairs(ctx, nzbDocumentID)
	if err != nil {
		return err
	}
	samples, err := db.loadNZBInteriorSampleSegments(ctx, nzbDocumentID, interiorSampleBudget)
	if err != nil {
		return err
	}
	if len(pairs) == 0 && len(samples) == 0 {
		return nil
	}
	checkCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	const maxConcurrentChecks = 8
	sem := make(chan struct{}, maxConcurrentChecks)
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	checkOne := func(messageID, pos string) {
		defer wg.Done()
		defer observability.Recover("nntp-preflight-check")
		select {
		case sem <- struct{}{}:
		case <-checkCtx.Done():
			return
		}
		defer func() { <-sem }()
		if err := checker.Exists(checkCtx, messageID); err != nil {
			errOnce.Do(func() {
				firstErr = sanitizedSegmentErr("preflight", pos, err, messageID)
				cancel()
			})
			return
		}
		slog.Debug("preflight: segment reachable", "messageID", messageID, "pos", pos)
	}
	for _, pair := range pairs {
		wg.Add(1)
		go checkOne(pair.first, "first")
		if pair.last != pair.first {
			wg.Add(1)
			go checkOne(pair.last, "last")
		}
	}
	for _, messageID := range samples {
		wg.Add(1)
		go checkOne(messageID, "interior sample")
	}
	wg.Wait()
	return firstErr
}

// loadNZBInteriorSampleSegments returns up to maxSamples evenly-spaced
// interior message IDs (excluding the first and last, already covered by
// loadNZBFirstLastSegmentPairs) from the document's qualifying files -- but
// only when exactly one qualifying file exists. A multi-file release (e.g. a
// RAR volume set) returns nil: sampling every volume at this density would
// reintroduce the exact per-candidate check-volume burst that made
// first/last-only the original design (see loadNZBFirstLastSegmentPairs).
func (db *DB) loadNZBInteriorSampleSegments(ctx context.Context, nzbDocumentID int64, maxSamples int) ([]string, error) {
	if maxSamples <= 0 {
		return nil, nil
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT nf.subject, nf.message_ids_packed, coalesce(nf.message_ids::text, '{}')
		FROM nzb_files nf
		WHERE nf.nzb_document_id = $1
		  AND (
		    nf.message_id_count > 0
		    OR coalesce(array_length(nf.message_ids, 1), 0) > 0
		  )
		ORDER BY nf.id ASC`, nzbDocumentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messageIDs []string
	qualifyingFiles := 0
	for rows.Next() {
		var subject, idsRaw string
		var packed []byte
		if err := rows.Scan(&subject, &packed, &idsRaw); err != nil {
			return nil, err
		}
		if !shouldValidateNZBSubject(subject) {
			continue
		}
		qualifyingFiles++
		if qualifyingFiles > 1 {
			continue // keep scanning to detect multi-file, but stop collecting
		}
		messageIDs, err = unpackMessageIDs(packed, idsRaw)
		if err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if qualifyingFiles != 1 || len(messageIDs) < 3 {
		return nil, nil
	}
	// Interior range excludes index 0 (first) and len-1 (last).
	interior := messageIDs[1 : len(messageIDs)-1]
	if len(interior) <= maxSamples {
		return interior, nil
	}
	step := float64(len(interior)) / float64(maxSamples)
	samples := make([]string, 0, maxSamples)
	for i := 0; i < maxSamples; i++ {
		samples = append(samples, interior[int(float64(i)*step)])
	}
	return samples, nil
}

// StrictCheckFirstSegments uses the older heavier validation strategy:
// download enough decoded article data to measure first/last segment sizes for
// every file, failing as soon as any required segment is unavailable.
func (db *DB) StrictCheckFirstSegments(ctx context.Context, nzbDocumentID int64) error {
	sizer, ok := db.SegmentFetcher.(SegmentSizer)
	if !ok || sizer == nil {
		return nil
	}
	pairs, err := db.loadNZBFirstLastSegmentPairs(ctx, nzbDocumentID)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if _, err := sizer.DecodedSize(ctx, p.first); err != nil {
			if errors.Is(err, yenc.ErrCRCMismatch) && !db.confirmPermanentCRCMismatch(ctx, p.first) {
				// Not confirmed on independent, delayed re-fetch -- treat as
				// the same transient glitch class confirmPermanentCRCMismatch
				// already guards calibration against, not permanent
				// corruption. Without this, the deep health check hard-failed
				// (and blocklisted) on a single unconfirmed CRC mismatch,
				// while the calibration path right above required two
				// independent, delayed, agreeing samples for the identical
				// failure class -- confirmed live, two false positives, on
				// this same provider.
				continue
			}
			return sanitizedSegmentErr("strict health", "first", err, p.first)
		}
		if p.last != p.first {
			if _, err := sizer.DecodedSize(ctx, p.last); err != nil {
				if errors.Is(err, yenc.ErrCRCMismatch) && !db.confirmPermanentCRCMismatch(ctx, p.last) {
					continue
				}
				return sanitizedSegmentErr("strict health", "last", err, p.last)
			}
		}
	}
	return nil
}

// messageIDSuffixPattern matches the trailing " [msgid:<...>]" annotation
// withMessageIDSuffix appends and splitMessageIDSuffix later strips off.
var messageIDSuffixPattern = regexp.MustCompile(`\s*\[msgid:([^\]]*)\]$`)

// withMessageIDSuffix annotates reason with messageID in a form that
// splitMessageIDSuffix can later recover, without changing anything that
// pattern-matches on the reason text itself (isConfirmedGoneArticleReason
// etc. all match substrings earlier in the string, unaffected by a trailing
// suffix). Returns reason unchanged if messageID is empty.
func withMessageIDSuffix(reason, messageID string) string {
	if messageID == "" {
		return reason
	}
	return reason + " [msgid:" + messageID + "]"
}

// splitMessageIDSuffix reverses withMessageIDSuffix: returns the reason text
// with any trailing message-id annotation removed, plus the annotated
// message-id (empty if none was present). Blocklist entries previously
// discarded which specific NNTP article failed a "confirmed gone" check, so
// a wrong verdict (e.g. a throttle-induced false 430) could never be
// re-checked or corrected later -- it was blocklisted forever on an
// unverifiable snapshot with nothing left to re-verify against.
func splitMessageIDSuffix(reason string) (cleanReason, messageID string) {
	m := messageIDSuffixPattern.FindStringSubmatch(reason)
	if m == nil {
		return reason, ""
	}
	return messageIDSuffixPattern.ReplaceAllString(reason, ""), m[1]
}

// sanitizedSegmentErr builds a blocklist-safe error for a failed segment
// check. The human-readable message omits the raw message ID (which would
// create one unique reason per segment) and strips trailing message IDs
// from the wrapped error text, but messageID itself travels alongside via
// withMessageIDSuffix so the eventual blocklist_items row can still record
// exactly which article failed (see splitMessageIDSuffix).
func sanitizedSegmentErr(kind, pos string, err error, messageID string) error {
	msg := err.Error()
	// Strip trailing bare message ID: "... (cached): msgid@host" → "... (cached)"
	if i := strings.LastIndex(msg, ": "); i > 0 {
		suffix := msg[i+2:]
		if strings.ContainsRune(suffix, '@') && !strings.ContainsRune(suffix, ' ') {
			msg = msg[:i]
		}
	}
	return errors.New(withMessageIDSuffix(fmt.Sprintf("%s: %s segment unavailable: %s", kind, pos, msg), messageID))
}

// CalibrateAllNZBOffsets runs CalibrateNZBOffsets for every NZB document in the
// database that has uncalibrated files. Called once at startup to fix any NZBs
// imported with the old estimated offset factor.
func (db *DB) CalibrateAllNZBOffsets(ctx context.Context) error {
	// Only select documents that have at least one uncalibrated file.
	// calibrated_at is set after a successful rescale, so NULL means uncalibrated.
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT DISTINCT nzb_document_id FROM nzb_files WHERE calibrated_at IS NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := func() error {
			docCtx, cancel := context.WithTimeout(ctx, perDocumentCalibrateBudget)
			defer cancel()
			return db.CalibrateNZBOffsets(docCtx, id)
		}(); err != nil {
			slog.Warn("calibrate all: failed for document", "nzb_document_id", id, "err", err)
		}
	}
	return nil
}

// perDocumentCalibrateBudget bounds how long CalibrateNZBOffsetsBatch spends
// on any single document -- matching workflow's asyncCalibrateBudget (the
// import-time calibration path already had this cap). Found live in the
// 2026-07-19 audit: without it, a document with many consecutive genuinely-
// corrupt segments could stall the whole periodic health-check batch for
// tens of minutes (confirmCRCMismatchAttempts * confirmCRCMismatchDelay per
// corrupt file, with no early-exit and no per-document cap), silently
// degrading the 15-minute maintenance cadence for every OTHER document still
// waiting in that batch.
// A var, not a const, so tests can shrink it instead of a real test run
// waiting out the full budget.
var perDocumentCalibrateBudget = 2 * time.Minute

// CalibrateNZBOffsetsBatch calibrates up to limit NZB documents that still have
// uncalibrated files. Returns the number of documents processed.
func (db *DB) CalibrateNZBOffsetsBatch(ctx context.Context, limit int) (int, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT DISTINCT nzb_document_id FROM nzb_files WHERE calibrated_at IS NULL LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		if err := func() error {
			docCtx, cancel := context.WithTimeout(ctx, perDocumentCalibrateBudget)
			defer cancel()
			return db.CalibrateNZBOffsets(docCtx, id)
		}(); err != nil {
			slog.Warn("calibrate batch: failed for document", "nzb_document_id", id, "err", err)
		}
	}
	return len(ids), nil
}

// isArticlePermanentlyMissing decides whether decodeErr (the error from a
// full DecodedSize fetch of messageID) reflects a permanent, never-going-
// to-change property of that specific article, as opposed to a transient
// fetch failure. Two distinct permanent cases:
//
//   - A yEnc CRC mismatch: the article exists (a STAT would succeed) but its
//     posted content is corrupt. Usenet articles are immutable, so
//     re-fetching the identical message-id decodes the identical bytes and
//     fails the identical CRC check forever -- this was previously missed
//     entirely (decodeErr was discarded in favor of a fresh STAT-only
//     check, which trivially succeeds for a corrupt-but-present article),
//     so calibrate.go retried these same known-corrupt segments on every
//     15-minute health-check pass, forever, each one a real live NNTP
//     fetch+decode.
//   - A confirmed-missing article (430 -> nntp.ErrArticleMissing), verified
//     via a cheap STAT (no body download). This also needs to be recognized
//     when served from CachedFallbackSource's local miss cache, not just a
//     live check -- see articleNotFoundError.Is in internal/nntp/article_cache.go.
//
// A timeout, throttled/circuit-open provider, connection error, or an
// inconclusive check (checker unavailable) falls through to "retry later"
// in both cases.
func (db *DB) isArticlePermanentlyMissing(ctx context.Context, messageID string, decodeErr error) bool {
	if errors.Is(decodeErr, yenc.ErrCRCMismatch) {
		return db.confirmPermanentCRCMismatch(ctx, messageID)
	}
	checker, ok := db.SegmentFetcher.(SegmentChecker)
	if !ok || checker == nil {
		return false
	}
	return errors.Is(checker.Exists(ctx, messageID), nntp.ErrArticleMissing)
}

// confirmCRCMismatchAttempts and confirmCRCMismatchDelay: how many delayed,
// independent re-fetches confirmPermanentCRCMismatch requires, all agreeing,
// before committing to "permanent". An immediate single re-fetch (the first
// version of this fix) turned out to be insufficient in production: two
// separate false positives were confirmed live where BOTH the original
// failure and an immediate confirmation retry got the same wrong result,
// while a fully independent manual refetch a minute or more later decoded
// perfectly cleanly with a matching CRC. Back-to-back attempts evidently can
// still hit the same transient condition (most likely a momentary backend
// inconsistency on the provider's side); spacing attempts out gives that a
// real chance to clear. Since Usenet articles are immutable, genuine
// corruption reproduces regardless of timing -- there's no downside to being
// more conservative here, only added latency for the rare corrupt/flaky case.
const confirmCRCMismatchAttempts = 2

// confirmCRCMismatchDelay is a var, not a const, so tests can shrink it
// instead of a real test run taking confirmCRCMismatchAttempts * 10s.
var confirmCRCMismatchDelay = 10 * time.Second

// confirmPermanentCRCMismatch re-fetches and re-decodes messageID
// independently, more than once with a delay between attempts, before
// concluding a CRC mismatch is a permanent property of the article rather
// than a transient glitch. See the STAT-based missing-article check below for
// the equivalent, independent confirmation it already gets for free from
// being a completely different mechanism -- the CRC path lacked any
// confirmation at all before this fix.
func (db *DB) confirmPermanentCRCMismatch(ctx context.Context, messageID string) bool {
	sizer, ok := db.SegmentFetcher.(SegmentSizer)
	if !ok || sizer == nil {
		// Can't re-fetch to confirm -- trust the single observation rather
		// than retrying forever with no way to ever resolve it.
		return true
	}
	for i := 0; i < confirmCRCMismatchAttempts; i++ {
		select {
		case <-time.After(confirmCRCMismatchDelay):
		case <-ctx.Done():
			// Can't wait out the delay -- don't commit to a permanent verdict
			// off an incomplete confirmation sequence.
			return false
		}
		_, err := sizer.DecodedSize(ctx, messageID)
		if !errors.Is(err, yenc.ErrCRCMismatch) {
			return false
		}
	}
	return true
}

// CalibrateNZBOffsets corrects segment decoded offsets for all files in an NZB
// document by fetching the first segment of each file and measuring its actual
// decoded size. This replaces the estimated offsets (0.74 or 0.97 factor) with
// values derived from the real yEnc payload size.
func (db *DB) CalibrateNZBOffsets(ctx context.Context, nzbDocumentID int64) error {
	sizer, ok := db.SegmentFetcher.(SegmentSizer)
	if !ok || sizer == nil {
		return nil
	}

	rows, err := db.SQL.QueryContext(ctx, `
		SELECT nf.id,
		       nf.message_ids_packed,
		       coalesce(nf.message_ids::text, '{}'),
		       nf.decoded_segment_size,
		       nf.last_decoded_size
		FROM nzb_files nf
		WHERE nf.nzb_document_id = $1
		  AND nf.calibrated_at IS NULL
		  AND (
		    nf.message_id_count > 0
		    OR coalesce(array_length(nf.message_ids, 1), 0) > 0
		  )`, nzbDocumentID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type fileInfo struct {
		id           int64
		firstMsgID   string
		estFirstSize int64
		lastMsgID    string
		estLastSize  int64
	}
	var files []fileInfo
	for rows.Next() {
		var f fileInfo
		var packed []byte
		var raw string
		if err := rows.Scan(&f.id, &packed, &raw, &f.estFirstSize, &f.estLastSize); err != nil {
			return err
		}
		ids, err := unpackMessageIDs(packed, raw)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			continue
		}
		f.firstMsgID = ids[0]
		f.lastMsgID = ids[len(ids)-1]
		if f.firstMsgID != "" && f.estFirstSize > 0 {
			files = append(files, f)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, f := range files {
		if ctx.Err() != nil {
			// Budget (perDocumentCalibrateBudget) or caller cancellation
			// expired -- stop attempting further files in this document now
			// rather than making each one fail-fast individually; they stay
			// calibrated_at IS NULL and are retried on a later pass.
			break
		}
		actualFirst, err := sizer.DecodedSize(ctx, f.firstMsgID)
		if err != nil {
			if !db.isArticlePermanentlyMissing(ctx, f.firstMsgID, err) {
				// Transient failure (timeout, provider throttle/circuit-open,
				// connection error, or the async import-time calibration
				// budget expiring mid-fetch) -- leave calibrated_at NULL so
				// a later CalibrateNZBOffsetsBatch pass retries. Previously
				// ANY error here permanently froze size_bytes at the
				// pre-calibration estimate, which then silently truncated
				// the served file (computeSpans uses size_bytes as the hard
				// end of the stream) -- reproducible for any release whose
				// first-segment fetch merely hiccuped once during import.
				slog.Warn("calibrate: could not fetch first segment, will retry later", "nzb_file_id", f.id, "err", err)
				continue
			}
			slog.Warn("calibrate: first segment permanently unusable (confirmed missing or corrupt) — marking as permanently skipped", "nzb_file_id", f.id, "err", err)
			// If this UPDATE itself fails (e.g. a transient DB error), the file is
			// NOT actually marked calibrated — log it, so a hung/lost mark-as-skipped
			// isn't invisible (it would otherwise just look like a normal retry).
			if _, updateErr := db.SQL.ExecContext(ctx, `UPDATE nzb_files SET calibrated_at = now() WHERE id = $1`, f.id); updateErr != nil {
				slog.Error("calibrate: failed to mark file as skipped", "nzb_file_id", f.id, "err", updateErr)
			}
			continue
		}
		if actualFirst <= 0 {
			continue
		}
		// Fetch the last segment to get the exact total file size by reading
		// its yEnc header directly. This avoids the compounding estimation
		// error for the last segment
		// that makes the total file size too large, which causes Plex to seek to
		// positions beyond the real end of file.
		actualLast := actualFirst // default: same size as other segments
		if f.lastMsgID != "" && f.lastMsgID != f.firstMsgID {
			n, err := sizer.DecodedSize(ctx, f.lastMsgID)
			switch {
			case err == nil && n > 0:
				actualLast = n
			case err != nil && !db.isArticlePermanentlyMissing(ctx, f.lastMsgID, err):
				// Same transient-vs-permanent distinction as the first-segment
				// fetch above: a failed fetch here previously fell through
				// silently, leaving actualLast at the "same size as other
				// segments" guess and still marking calibrated_at via
				// rescaleFileSegments below -- permanently freezing a guess
				// that's frequently wrong (a real last segment is often a
				// remainder chunk, not a full one) with no way to ever retry.
				// Leave calibrated_at NULL so a later pass retries both
				// segments fresh instead.
				slog.Warn("calibrate: could not fetch last segment, will retry later", "nzb_file_id", f.id, "err", err)
				continue
			case err != nil:
				slog.Warn("calibrate: last segment confirmed missing (NNTP STAT 430) — using first-segment size as the best available estimate", "nzb_file_id", f.id, "err", err)
			}
		}
		if err := db.rescaleFileSegments(ctx, f.id, actualFirst, actualLast); err != nil {
			return fmt.Errorf("rescale nzb_file %d: %w", f.id, err)
		}
		slog.Info("calibrate: corrected segment offsets",
			"nzb_file_id", f.id,
			"estimated_first", f.estFirstSize,
			"actual_first", actualFirst,
			"actual_last", actualLast)
	}
	return nil
}

// rescaleFileSegments updates decoded segment sizes for a file inline in nzb_files
// and recomputes virtual_files.size_bytes for any direct_nzb virtual file backed
// by this nzb_file.  The old nzb_segments / virtual_file_ranges tables were
// removed by migration 000041; all segment data now lives in nzb_files.
//
// actualFirstSize is the measured decoded size of segment 1 (applied uniformly
// to all non-last segments). actualLastSize is the measured decoded size of the
// final segment — using the real value avoids the file-size overestimation that
// causes Plex to seek past the real end of file (the last segment's yEnc
// header gives an exact total size instead of relying on the estimate).
func (db *DB) rescaleFileSegments(ctx context.Context, nzbFileID, actualFirstSize, actualLastSize int64) error {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Lock the owning library item's queue_items row FIRST, in the same
	// order every function that deletes/inserts selected_releases rows
	// already uses (see lockLibraryItemQueueRow's own doc comment).
	// Without this, this transaction's UPDATEs on nzb_files/virtual_files
	// below can deadlock against FailSelectedReleaseAndPromoteNext's
	// cascading DELETE FROM selected_releases, which locks those exact
	// same nzb_files/virtual_files rows (via ON DELETE CASCADE through
	// nzb_documents) in whatever order Postgres's cascade happens to visit
	// them -- confirmed live (2026-08-11): "rescale nzb_file N: ERROR:
	// deadlock detected (SQLSTATE 40P01)" firing repeatedly in production,
	// roughly every 15-30 minutes, and at least once hitting the promote
	// side too ("workqueue: promote retry failed"). Acquiring the same
	// queue_items lock first establishes a single consistent lock order
	// between the two paths instead of leaving Postgres's cascade order
	// (which application code doesn't control) to decide it. A missing
	// owning library item (concurrent delete raced this calibration pass)
	// isn't an error here -- there's nothing left to conflict with.
	var libraryItemID int64
	lockErr := tx.QueryRowContext(ctx, `
		select sr.library_item_id
		from nzb_files nf
		join nzb_documents nd on nd.id = nf.nzb_document_id
		join selected_releases sr on sr.id = nd.selected_release_id
		where nf.id = $1`, nzbFileID,
	).Scan(&libraryItemID)
	if lockErr != nil && !errors.Is(lockErr, sql.ErrNoRows) {
		return lockErr
	}
	if lockErr == nil {
		if lockErr = lockLibraryItemQueueRow(ctx, tx, libraryItemID); lockErr != nil {
			return lockErr
		}
	}

	// Update the inline segment sizes stored in nzb_files.
	// COALESCE guards against array_length returning NULL for an empty array.
	var segmentCount int64
	if err = tx.QueryRowContext(ctx, `
		UPDATE nzb_files
		SET decoded_segment_size = $2,
		    last_decoded_size     = $3
		WHERE id = $1
		RETURNING case
			when message_id_count > 0 then message_id_count
			else coalesce(array_length(message_ids, 1), 0)
		end`,
		nzbFileID, actualFirstSize, actualLastSize,
	).Scan(&segmentCount); err != nil {
		return err
	}

	// Recompute virtual_files.size_bytes for direct_nzb entries backed by this
	// nzb_file.  The total decoded size is:
	//   (segmentCount - 1) * actualFirstSize + actualLastSize
	// This exactly mirrors what computeSpans produces.
	if segmentCount > 0 {
		totalSize := (segmentCount-1)*actualFirstSize + actualLastSize
		if _, err = tx.ExecContext(ctx, `
			UPDATE virtual_files
			SET size_bytes = $2
			WHERE nzb_file_id = $1
			  AND reader_kind = 'direct_nzb'`,
			nzbFileID, totalSize,
		); err != nil {
			return err
		}
	}

	// Mark this file as calibrated so future startup passes can skip it.
	if _, err = tx.ExecContext(ctx, `UPDATE nzb_files SET calibrated_at = now() WHERE id = $1`, nzbFileID); err != nil {
		return err
	}

	if err = tx.Commit(); err != nil {
		return err
	}

	// Flush the in-memory VF cache so the next open picks up the new sizes.
	db.InvalidateVFCacheForNZBFile(nzbFileID)
	return nil
}

// PublishedDirectNzbSegment holds the identifiers needed to validate and reset
// a published direct_nzb virtual file.
type PublishedDirectNzbSegment struct {
	LibraryItemID     int64
	FirstMsgID        string
	SelectedReleaseID int64
}

// ListPublishedDirectNzbSegments returns one entry per library item that has an
// active symlink_publication backed by a direct_nzb virtual file. Only the
// first message ID is returned — if that segment is missing the whole release
// is considered unplayable.
func (db *DB) ListPublishedDirectNzbSegments(ctx context.Context) ([]PublishedDirectNzbSegment, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT
		    sp.library_item_id,
		    nf.message_ids_packed,
		    coalesce(nf.message_ids::text, '{}'),
		    coalesce(qi.selected_release_id, 0)
		FROM symlink_publications sp
		JOIN virtual_files vf ON vf.id = sp.virtual_file_id
		JOIN nzb_files nf ON nf.id = vf.nzb_file_id
		LEFT JOIN queue_items qi ON qi.library_item_id = sp.library_item_id
		WHERE vf.reader_kind = 'direct_nzb'
		  AND (
		    nf.message_id_count > 0
		    OR coalesce(array_length(nf.message_ids, 1), 0) > 0
		  )
		ORDER BY sp.library_item_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PublishedDirectNzbSegment
	seen := make(map[int64]struct{})
	for rows.Next() {
		var s PublishedDirectNzbSegment
		var packed []byte
		var raw string
		if err := rows.Scan(&s.LibraryItemID, &packed, &raw, &s.SelectedReleaseID); err != nil {
			return nil, err
		}
		if _, ok := seen[s.LibraryItemID]; ok {
			continue
		}
		ids, err := unpackMessageIDs(packed, raw)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			continue
		}
		s.FirstMsgID = ids[0]
		out = append(out, s)
		seen[s.LibraryItemID] = struct{}{}
	}
	return out, rows.Err()
}
