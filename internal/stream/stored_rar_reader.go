package stream

import (
	"context"
	"errors"
	"io"
	"sync"
)

var ErrStoredRarLayoutInvalid = errors.New("stored_rar layout invalid")

type StoredRarReader struct {
	name    string
	size    int64
	spans   []SegmentSpan
	fetcher SegmentFetcher
	manager *ReadAheadManager
	mu      sync.Mutex
}

func NewStoredRarReader(name string, size int64, spans []SegmentSpan, fetcher SegmentFetcher, manager *ReadAheadManager) *StoredRarReader {
	reader := &StoredRarReader{
		name:    name,
		size:    size,
		fetcher: fetcher,
		manager: manager,
	}
	reader.spans = append(reader.spans, spans...)
	return reader
}

func (r *StoredRarReader) Name() string {
	return r.name
}

func (r *StoredRarReader) Size() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

func (r *StoredRarReader) snapshot() ([]SegmentSpan, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spans, r.size
}

func (r *StoredRarReader) ReadAt(ctx context.Context, dst []byte, offset int64) (int, error) {
	spans, size := r.snapshot()
	if err := validateStoredRarSpans(spans, size); err != nil {
		return 0, err
	}
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
		spans, size = r.snapshot()
		if current >= size {
			break
		}
		ranges, err := ResolveRange(spans, current, min64(length-int64(written), size-current))
		if err != nil {
			if written > 0 {
				return written, io.EOF
			}
			return written, err
		}
		if len(ranges) == 0 {
			break
		}
		// Only the first resolved range is used per iteration -- spans are
		// re-snapshotted fresh every loop so a realignment below (correcting
		// a wrong decoded-segment-size estimate) is picked up immediately by
		// the next range resolved, instead of continuing to use the rest of
		// a batch resolved against now-stale span data.
		rng := ranges[0]
		// Translate VF byte positions to decoded-segment byte positions.
		// rng.RangeStart/End are in VF space; the NNTP fetcher expects archive
		// (decoded) byte positions. The offset into the decoded segment is:
		//   segment_byte_start + (vf_pos - span_vf_start)
		// where segment_byte_start accounts for the RAR header that precedes the
		// embedded file in the first segment of each volume.
		segOffset := rng.SegmentByteStart + (rng.RangeStart - rng.SegmentStart)
		reqLength := rng.RangeEnd - rng.RangeStart
		adj := SegmentRange{
			SegmentID:    rng.SegmentID,
			MessageID:    rng.MessageID,
			RangeStart:   rng.DecodedStart + segOffset,
			RangeEnd:     rng.DecodedStart + segOffset + reqLength,
			SegmentStart: rng.DecodedStart,
			SegmentEnd:   rng.DecodedStart + (rng.SegmentEnd - rng.SegmentStart) + rng.SegmentByteStart,
		}
		var (
			block      []byte
			actualSpan SegmentSpan
			hasActual  bool
			err2       error
		)
		if aware, ok := r.fetcher.(interface {
			FetchRangeInfo(ctx context.Context, segment SegmentRange) ([]byte, SegmentSpan, error)
		}); ok {
			block, actualSpan, err2 = aware.FetchRangeInfo(ctx, adj)
			hasActual = actualSpan.MessageID != ""
		} else {
			block, err2 = r.fetcher.FetchRange(ctx, adj)
		}
		expected := int(reqLength)
		if err2 == nil && len(block) < expected && hasActual {
			// The calibrated decoded_segment_size/last_decoded_size estimate
			// for this specific NNTP segment doesn't match its true decoded
			// size (confirmed live in production: this hits almost
			// exclusively the last segment of the last volume, since last
			// segments are the hardest to estimate and truncateSpans only
			// reconciles the aggregate total against virtual_files.size_bytes,
			// not each individual segment's own boundaries). Correct this
			// span from the fetcher's real measurement and retry, the same
			// self-healing DirectNzbReader already does via realignSpans --
			// without this, a several-hundred-byte estimate error on one
			// segment silently truncated the entire served stream right at
			// that point, which for the last segment meant every Range
			// request landing near real EOF (exactly where a player probes
			// for trailing container metadata) got a short/empty read.
			if r.realignSpan(rng.SegmentID, actualSpan) {
				continue
			}
		}
		if err2 != nil {
			return written, err2
		}
		if len(block) < expected {
			if len(block) == 0 {
				emptyCount++
				_, curSize := r.snapshot()
				if emptyCount > 5 || current >= curSize {
					if written > 0 {
						return written, io.EOF
					}
					return 0, io.EOF
				}
				continue
			}
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

// realignSpan corrects the span identified by segmentID using the fetcher's
// real measurement of that NNTP article's decoded boundaries, then shifts
// every later span's VF-relative position by the resulting delta. Only ever
// shrinks a span (never grows it beyond its original length): the confirmed
// failure mode is an over-estimate, and letting a span grow would risk
// overlapping the VF-space range already assigned to the next span. Reports
// whether a correction was actually made (false if segmentID wasn't found,
// e.g. a concurrent realignment already ran).
func (r *StoredRarReader) realignSpan(segmentID int64, actual SegmentSpan) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := -1
	for i := range r.spans {
		if r.spans[i].SegmentID == segmentID {
			index = i
			break
		}
	}
	if index < 0 {
		return false
	}
	old := r.spans[index]
	available := actual.End - actual.Start - old.SegmentByteStart
	if available < 0 {
		available = 0
	}
	newLen := old.End - old.Start
	if available < newLen {
		newLen = available
	}
	if old.DecodedStart == actual.Start && old.End-old.Start == newLen {
		return false // nothing to correct; avoid an infinite retry loop
	}
	r.spans[index].DecodedStart = actual.Start
	r.spans[index].End = old.Start + newLen
	delta := r.spans[index].End - old.End
	if delta != 0 {
		for i := index + 1; i < len(r.spans); i++ {
			r.spans[i].Start += delta
			r.spans[i].End += delta
		}
	}
	r.size = r.spans[len(r.spans)-1].End
	return true
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (r *StoredRarReader) StartSession(sessionID string) {
	if r == nil || r.manager == nil {
		return
	}
	fetcher, ok := r.fetcher.(PrioritySegmentFetcher)
	if !ok {
		return
	}
	spans, _ := r.snapshot()
	r.manager.Register(sessionID, spans, fetcher)
}

func (r *StoredRarReader) NotifyRead(sessionID string, offset int64) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.NotifyRead(sessionID, offset)
}

func (r *StoredRarReader) Seek(sessionID string, offset int64) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.Seek(sessionID, offset)
}

func (r *StoredRarReader) StopSession(sessionID string) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.Stop(sessionID)
}

func (r *StoredRarReader) RegisterMeta(sessionID string, meta SessionMeta) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.RegisterMeta(sessionID, meta)
}

func validateStoredRarSpans(spans []SegmentSpan, size int64) error {
	if size < 0 {
		return ErrStoredRarLayoutInvalid
	}
	if size == 0 {
		return nil
	}
	if len(spans) == 0 {
		return ErrStoredRarLayoutInvalid
	}
	expectedStart := int64(0)
	for _, span := range spans {
		if span.Start != expectedStart || span.End <= span.Start {
			return ErrStoredRarLayoutInvalid
		}
		expectedStart = span.End
	}
	if expectedStart != size {
		return ErrStoredRarLayoutInvalid
	}
	return nil
}
