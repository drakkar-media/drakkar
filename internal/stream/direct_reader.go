package stream

import (
	"context"
	"io"
	"sort"
	"sync"
)

type SegmentFetcher interface {
	FetchRange(ctx context.Context, segment SegmentRange) ([]byte, error)
}

type DirectNzbReader struct {
	name    string
	size    int64
	spans   []SegmentSpan
	fetcher SegmentFetcher
	manager *ReadAheadManager
	mu      sync.Mutex
}

func NewDirectNzbReader(name string, size int64, spans []SegmentSpan, fetcher SegmentFetcher, manager *ReadAheadManager) *DirectNzbReader {
	return &DirectNzbReader{name: name, size: size, spans: spans, fetcher: fetcher, manager: manager}
}

func (r *DirectNzbReader) Name() string {
	return r.name
}

func (r *DirectNzbReader) Size() int64 {
	return r.currentSize()
}

// currentSize reads r.size under r.mu -- realignSpans updates size and spans
// together while holding the same lock (direct_reader.go:136-156), so any
// unguarded read here is a real data race and can also silently use a stale
// value: confirmed live (2026-07-25) as the cause of intermittent decode
// corruption during playback. Unlike StoredRarReader.ReadAt (which
// re-snapshots size/spans every loop iteration via snapshot()), this
// function used to read r.size exactly once, unguarded, at the very top and
// never refreshed it -- if a concurrent ReadAt on the same reader instance
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

func (r *DirectNzbReader) ReadAt(ctx context.Context, dst []byte, offset int64) (int, error) {
	size := r.currentSize()
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
		emptyCount = 0
		copy(dst[written:written+len(block)], block)
		written += len(block)
		current += int64(len(block))
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

func (r *DirectNzbReader) StartSession(sessionID string) {
	if r == nil || r.manager == nil {
		return
	}
	fetcher, ok := r.fetcher.(PrioritySegmentFetcher)
	if !ok {
		return
	}
	r.manager.Register(sessionID, r.spans, fetcher)
}

func (r *DirectNzbReader) NotifyRead(sessionID string, offset int64) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.NotifyRead(sessionID, offset)
}

func (r *DirectNzbReader) Seek(sessionID string, offset int64) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.Seek(sessionID, offset)
}

func (r *DirectNzbReader) StopSession(sessionID string) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.Stop(sessionID)
}

func (r *DirectNzbReader) RegisterMeta(sessionID string, meta SessionMeta) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.RegisterMeta(sessionID, meta)
}
