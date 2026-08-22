package stream

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
)

// SegmentFetcher defines the minimal operation required to retrieve a byte
// range from an NNTP segment.
//
// Implementations must be safe for concurrent use, since a single reader can
// have multiple ReadAt calls and read-ahead fetches in flight at once.
type SegmentFetcher interface {
	FetchRange(ctx context.Context, segment SegmentRange) ([]byte, error)
}

// DirectNzbReader is a VirtualMediaFile backed directly by a multi-segment
// NZB (as opposed to a file embedded inside a stored RAR volume, see
// StoredRarReader). It resolves a requested byte range to the underlying
// segments via spans, fetching missing data through fetcher.
//
// size and spans are self-correcting: the yEnc-decoded size of a segment is
// only known precisely once it has actually been fetched, so an initial
// estimate can be wrong. realignSpans corrects size/spans in place once a
// fetch reveals the segment's true boundaries, shifting every later span by
// the resulting delta to keep the layout contiguous. All access to size and
// spans must go through mu; DirectNzbReader is safe for concurrent ReadAt
// calls.
type DirectNzbReader struct {
	name    string
	size    int64
	spans   []SegmentSpan
	fetcher SegmentFetcher
	manager *ReadAheadManager
	mu      sync.Mutex
}

// NewDirectNzbReader creates a DirectNzbReader for a file spanning the given
// segments. spans is retained and mutated in place by realignSpans as
// segments are fetched and their true boundaries become known, so callers
// must not continue to read or mutate the slice after passing it in.
func NewDirectNzbReader(name string, size int64, spans []SegmentSpan, fetcher SegmentFetcher, manager *ReadAheadManager) *DirectNzbReader {
	return &DirectNzbReader{name: name, size: size, spans: spans, fetcher: fetcher, manager: manager}
}

// Name returns the file's display name.
func (r *DirectNzbReader) Name() string {
	return r.name
}

// Size returns the file's current size, which may have been corrected since
// construction as realignSpans learned segments' true decoded boundaries.
func (r *DirectNzbReader) Size() int64 {
	return r.currentSize()
}

// currentSize reads r.size under r.mu -- realignSpans updates size and spans
// together while holding the same lock (direct_reader.go:136-156), so any
// unguarded read here is a real data race and can also silently use a stale
// value: confirmed live (2026-07-25) as the cause of intermittent decode
// corruption during playback. Unlike StoredRarReader.ReadAt (which loads an
// immutable atomic state version each loop), this function used to read
// r.size exactly once, unguarded, at the very top and never refreshed it --
// if a concurrent ReadAt on the same reader instance
// (e.g. Plex issuing overlapping Range requests) triggered a mid-flight
// yEnc-offset recalibration via realignSpans, this call's cached `length`
// bound could go stale relative to the now-shifted spans/size, letting the
// loop keep copying segment data past where the corrected layout says this
// read should have stopped -- bytes from the wrong logical file offset
// landing in the response, decoded by the player as garbage pixels.
func (r *DirectNzbReader) currentSize() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// ReadAt fills dst with up to len(dst) bytes starting at offset, fetching
// whatever underlying NNTP segments are needed to satisfy the range.
//
// A short read paired with io.EOF means offset+len(dst) reached or exceeded
// the file's current size; any other error aborts with whatever was written
// so far. If a fetch reveals that a span's real boundaries differ from the
// estimate used to plan the request (self-correction via realignSpans), the
// loop re-resolves the current position against the corrected layout instead
// of returning the (possibly now-empty or misaligned) block, up to a small
// retry budget (emptyCount) to avoid spinning forever if a segment
// genuinely has no data left to give at this position.
//
// Safe for concurrent use; concurrent ReadAt calls on the same reader may
// each trigger and observe realignment independently.
func (r *DirectNzbReader) ReadAt(ctx context.Context, dst []byte, offset int64) (int, error) {
	size := r.currentSize()
	slog.Debug("dnr ReadAt call", "name", r.name, "offset", offset, "len(dst)", len(dst), "size", size)
	if offset >= size {
		return 0, io.EOF
	}
	length := int64(len(dst))
	if offset+length > size {
		length = size - offset
	}
	written := 0
	current := offset
	emptyCount := 0
	for int64(written) < length {
		if current >= r.currentSize() {
			break
		}
		span, index, err := r.findSpan(current)
		if err != nil {
			if written > 0 {
				return written, io.EOF
			}
			return written, err
		}
		requestEnd := span.End
		finalEnd := offset + length
		if requestEnd > finalEnd {
			requestEnd = finalEnd
		}
		req := SegmentRange{
			SegmentID:    span.SegmentID,
			MessageID:    span.MessageID,
			RangeStart:   current,
			RangeEnd:     requestEnd,
			SegmentStart: span.Start,
			SegmentEnd:   span.End,
		}
		var (
			block      []byte
			actualSpan SegmentSpan
		)
		if aware, ok := r.fetcher.(interface {
			FetchRangeInfo(ctx context.Context, segment SegmentRange) ([]byte, SegmentSpan, error)
		}); ok {
			block, actualSpan, err = aware.FetchRangeInfo(ctx, req)
		} else {
			block, err = r.fetcher.FetchRange(ctx, req)
			actualSpan = span
		}
		if err != nil {
			return written, err
		}
		if !r.spanUnchanged(index, span) {
			r.realignSpans(index, actualSpan)
			continue
		}
		r.realignSpans(index, actualSpan)
		expected := int(requestEnd - current)
		if len(block) == 0 {
			// realignSpans just corrected the span boundaries based on actual yEnc
			// offsets. The requested position may now fall in a different span —
			// retry findSpan rather than returning EOF immediately.
			emptyCount++
			if emptyCount > 5 || current >= r.currentSize() {
				if written > 0 {
					return written, io.EOF
				}
				return 0, io.EOF
			}
			continue
		}
		if len(block) < expected {
			// A non-zero but short block here (as opposed to the realignSpans
			// correction above, which already reconciled the span boundaries)
			// means the fetch itself returned less than the segment's own
			// resolved range promised -- an unexplained truncation, not a
			// boundary estimate being wrong. StoredRarReader treats this
			// identically (see its "short fetch" error): silently accepting it
			// and advancing current by len(block) would let a lenient MKV
			// demuxer absorb the gap as a dropped/duplicated frame instead of
			// surfacing the underlying fetch problem.
			return written, errors.New("short fetch")
		}
		emptyCount = 0
		copy(dst[written:written+expected], block[:expected])
		written += expected
		current += int64(expected)
	}
	if int64(written) < int64(len(dst)) {
		return written, io.EOF
	}
	return written, nil
}

func (r *DirectNzbReader) findSpan(offset int64) (SegmentSpan, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Binary search — spans are sorted and contiguous. O(log N) vs O(N) for
	// large files (a 2 GB movie has ~2800 segments; linear scan wastes seek time).
	n := len(r.spans)
	i := sort.Search(n, func(i int) bool { return r.spans[i].End > offset })
	if i < n && r.spans[i].Start <= offset {
		return r.spans[i], i, nil
	}
	var first, last SegmentSpan
	if n > 0 {
		first, last = r.spans[0], r.spans[n-1]
	}
	slog.Debug("dnr findSpan miss", "name", r.name, "offset", offset, "size", r.size,
		"spanCount", n, "searchIndex", i, "firstSpan", first, "lastSpan", last)
	return SegmentSpan{}, -1, ErrRangeOutsideFile
}

// spanUnchanged reports whether r.spans[index] still matches expected —
// used to detect a concurrent realignSpans call that shifted this span's
// offsets while a fetch for it was in flight.
func (r *DirectNzbReader) spanUnchanged(index int, expected SegmentSpan) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if index < 0 || index >= len(r.spans) {
		return false
	}
	return r.spans[index].Start == expected.Start && r.spans[index].End == expected.End
}

// realignSpans replaces the span at index with actual (the fetcher's real
// measurement of that segment's decoded boundaries) and shifts every later
// span's Start/End by the resulting delta, preserving contiguity. It also
// updates r.size to match the new end of the last span, since a size
// estimated before any segment was fetched can be wrong in either direction.
// No-op if index is out of range (e.g. a concurrent realignment already
// shrank spans past it).
func (r *DirectNzbReader) realignSpans(index int, actual SegmentSpan) {
	if index < 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if index >= len(r.spans) {
		return
	}
	old := r.spans[index]
	r.spans[index] = actual
	delta := actual.End - old.End
	if delta == 0 {
		return
	}
	for i := index + 1; i < len(r.spans); i++ {
		r.spans[i].Start += delta
		r.spans[i].End += delta
	}
	r.size = r.spans[len(r.spans)-1].End
}

// StartSession registers this reader with its ReadAheadManager under
// sessionID, enabling read-ahead prefetch for the stream. A no-op if the
// reader has no manager or its fetcher doesn't support prioritized fetches.
func (r *DirectNzbReader) StartSession(sessionID string) {
	if r == nil || r.manager == nil {
		return
	}
	fetcher, ok := r.fetcher.(PrioritySegmentFetcher)
	if !ok {
		return
	}
	// Snapshot under r.mu rather than passing r.spans directly -- found
	// during the 2026-07-25 corruption audit as the same unguarded-shared-
	// array class already fixed in ReadAt/realignSpans and by
	// StoredRarReader's immutable state; not currently reachable (the sole caller
	// calls this synchronously before the reader is ever read from), but
	// left unguarded here would silently reopen the same hazard the moment
	// that assumption changes.
	r.mu.Lock()
	spans := make([]SegmentSpan, len(r.spans))
	copy(spans, r.spans)
	r.mu.Unlock()
	r.manager.Register(sessionID, spans, fetcher)
}

// NotifyRead reports the current read position for sessionID to the
// ReadAheadManager, which uses it to schedule the next prefetch window.
func (r *DirectNzbReader) NotifyRead(sessionID string, offset int64) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.NotifyRead(sessionID, offset)
}

// Seek cancels any in-flight read-ahead window for sessionID without
// scheduling a new one at offset; the next NotifyRead (from the interactive
// read that follows a seek) schedules read-ahead from the correct position.
func (r *DirectNzbReader) Seek(sessionID string, offset int64) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.Seek(sessionID, offset)
}

// StopSession ends the read-ahead session for sessionID and cancels any
// in-flight prefetch for it.
func (r *DirectNzbReader) StopSession(sessionID string) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.Stop(sessionID)
}

// RegisterMeta attaches display metadata to the session's read-ahead entry.
func (r *DirectNzbReader) RegisterMeta(sessionID string, meta SessionMeta) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.RegisterMeta(sessionID, meta)
}
