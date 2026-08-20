package stream

import (
	"context"
	"errors"
	"io"
	"sort"
	"sync"
	"sync/atomic"
)

// ErrStoredRarLayoutInvalid is returned when a StoredRarReader's spans don't
// form a valid, contiguous [0, size) layout — see validateStoredRarSpans.
var ErrStoredRarLayoutInvalid = errors.New("stored_rar layout invalid")

// StoredRarReader is a VirtualMediaFile for a file embedded inside a
// multi-volume stored (uncompressed) RAR archive. Unlike DirectNzbReader,
// each span additionally carries the offset of the embedded file's content
// within its NNTP segment's decoded bytes (DecodedStart/SegmentByteStart),
// since a stored-RAR segment mixes RAR volume/file headers with raw file
// data.
//
// Its span layout is immutable between realignments. Readers load one atomic
// state pointer without copying the complete span table; realignSpan publishes
// a corrected copy while retaining old versions for reads already in flight.
// StoredRarReader is safe for concurrent ReadAt calls.
type StoredRarReader struct {
	name    string
	fetcher SegmentFetcher
	manager *ReadAheadManager
	state   atomic.Pointer[storedRarSpanState]
	stateMu sync.Mutex
}

// storedRarSpanState is an immutable, versioned virtual-file layout. Published
// states and their span slices must never be mutated.
type storedRarSpanState struct {
	spans       []SegmentSpan
	size        int64
	version     uint64
	layoutValid bool
}

// NewStoredRarReader creates a StoredRarReader for a file embedded across the
// given segment spans. spans is copied, so the caller's slice may be reused
// or mutated after this call.
func NewStoredRarReader(name string, size int64, spans []SegmentSpan, fetcher SegmentFetcher, manager *ReadAheadManager) *StoredRarReader {
	reader := &StoredRarReader{
		name:    name,
		fetcher: fetcher,
		manager: manager,
	}
	stateSpans := append([]SegmentSpan(nil), spans...)
	reader.state.Store(&storedRarSpanState{
		spans:       stateSpans,
		size:        size,
		layoutValid: validateStoredRarSpans(stateSpans, size) == nil,
	})
	return reader
}

// Name returns the file's display name.
func (r *StoredRarReader) Name() string {
	return r.name
}

// Size returns the file's current size, which may have been corrected since
// construction as realignSpan learned segments' true decoded boundaries.
func (r *StoredRarReader) Size() int64 {
	return r.state.Load().size
}

// ReadAt fills dst with up to len(dst) bytes starting at offset (in this
// virtual file's byte space), translating each resolved span into the
// corresponding byte range of its underlying NNTP segment before fetching.
//
// Errors:
//   - ErrStoredRarLayoutInvalid: the reader's spans don't form a valid,
//     contiguous [0, size) layout (see validateStoredRarSpans).
//   - io.EOF: offset+len(dst) reached or exceeded the file's current size;
//     any bytes written before EOF are still returned.
//
// A short fetch for a span is first checked against the fetcher's actual
// measurement of that segment's boundaries: if the segment's true decoded
// size differs from the estimate used to build the span (self-correction via
// realignSpan), the read retries against the corrected layout rather than
// treating the short fetch as an error or premature EOF. A short fetch that
// isn't explained by a boundary correction is a genuine error.
//
// Safe for concurrent use; concurrent ReadAt calls on the same reader may
// each trigger and observe realignment independently.
func (r *StoredRarReader) ReadAt(ctx context.Context, dst []byte, offset int64) (int, error) {
	state := r.state.Load()
	if !state.layoutValid {
		return 0, ErrStoredRarLayoutInvalid
	}
	if offset < 0 {
		return 0, ErrRangeOutsideFile
	}
	if offset >= state.size {
		return 0, io.EOF
	}
	length := int64(len(dst))
	if remaining := state.size - offset; length > remaining {
		length = remaining
	}
	written := 0
	current := offset
	emptyCount := 0
	for int64(written) < length {
		state = r.state.Load()
		if !state.layoutValid {
			return written, ErrStoredRarLayoutInvalid
		}
		if current >= state.size {
			break
		}
		span, err := findStoredRarSpan(state.spans, current)
		if err != nil {
			if written > 0 {
				return written, io.EOF
			}
			return written, err
		}
		requestEnd := span.End
		remaining := length - int64(written)
		if stateRemaining := state.size - current; remaining > stateRemaining {
			remaining = stateRemaining
		}
		if finalEnd := current + remaining; requestEnd > finalEnd {
			requestEnd = finalEnd
		}
		// Translate VF byte positions to decoded-segment byte positions.
		// current/requestEnd are in VF space; the NNTP fetcher expects archive
		// (decoded) byte positions. The offset into the decoded segment is:
		//   segment_byte_start + (vf_pos - span_vf_start)
		// where segment_byte_start accounts for the RAR header that precedes the
		// embedded file in the first segment of each volume.
		segOffset := span.SegmentByteStart + (current - span.Start)
		reqLength := requestEnd - current
		adj := SegmentRange{
			SegmentID:    span.SegmentID,
			MessageID:    span.MessageID,
			RangeStart:   span.DecodedStart + segOffset,
			RangeEnd:     span.DecodedStart + segOffset + reqLength,
			SegmentStart: span.DecodedStart,
			SegmentEnd:   span.DecodedStart + (span.End - span.Start) + span.SegmentByteStart,
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
		// A concurrent correction can shift this virtual offset into another
		// segment while the network fetch is in flight. Retry against the new
		// immutable version instead of consuming bytes resolved from stale state.
		if r.state.Load().version != state.version {
			continue
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
			if r.realignSpan(span.SegmentID, actualSpan) || r.state.Load().version != state.version {
				continue
			}
		}
		if err2 == nil && len(block) == expected && hasActual && requestEnd == span.End {
			// The fetch fully satisfied the request, but that alone doesn't
			// rule out an UNDER-estimate: the assumed span may simply not
			// have asked for enough (a short fetch, the only other trigger
			// for realignSpan above, can never happen in that case). Cross-
			// check the fetcher's real measurement of this segment's
			// boundaries -- confirmed live 2026-08-20 (a 2160p HEVC release
			// embedded in a stored RAR volume): when the true decoded
			// payload for a segment is LARGER than its estimated span, the
			// span kept its old, too-short length forever, since nothing
			// here ever asked for more than that estimate allowed and the
			// fetch always came back fully satisfied. Reads past that point
			// resolved against the NEXT segment's own DecodedStart/
			// SegmentByteStart instead of the true remaining tail of this
			// one -- bytes spliced in from the wrong segment, decoded as a
			// block of corrupted pixels severe enough to desync playback
			// until a fresh keyframe read (a seek) recovered it. block
			// already holds the correct bytes for this request regardless
			// of the correction, so grow the span for future iterations and
			// fall through to consume it normally rather than re-fetching.
			if available := actualSpan.End - actualSpan.Start - span.SegmentByteStart; available > span.End-span.Start {
				r.realignSpan(span.SegmentID, actualSpan)
			}
		}
		if err2 != nil {
			return written, err2
		}
		if len(block) < expected {
			if len(block) == 0 {
				emptyCount++
				if emptyCount > 5 || current >= r.state.Load().size {
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
// every later span's VF-relative position by the resulting delta. Corrects
// in either direction, mirroring DirectNzbReader.realignSpans: the
// segment's true decoded size, only known once actually fetched, can be
// either smaller OR larger than the estimate used to build the span.
// old.SegmentByteStart (the RAR volume-header skip, a fixed quantity set at
// NZB-build time from the volume header's own byte length) is independent
// of the segment's total decoded size in either direction, so it remains
// valid regardless of which way this correction goes.
//
// Confirmed live 2026-08-20 (a 2160p HEVC release embedded in a stored RAR
// volume): this used to clamp newLen down to available but never let it
// grow past the original estimate ("the confirmed failure mode is an
// over-estimate" -- true for
// whatever incident originally motivated that clamp, but not a guarantee
// for every release). For an UNDER-estimated segment, the span kept its
// old, too-short length, so ReadAt calls past that point resolved against
// the NEXT segment's own DecodedStart/SegmentByteStart instead of the true
// remaining tail of the current one -- bytes spliced in from the wrong
// segment at that exact position, decoded as a block of corrupted pixels,
// severe enough to desync the HEVC bitstream until a fresh keyframe read
// (a seek) recovered it. Growing shifts every later span by the same
// +delta the shrink path already used for a negative delta, preserving
// contiguity identically in either direction (spans[index].End ==
// spans[index+1].Start still holds after a uniform shift, regardless of
// delta's sign) -- there is no overlap risk the shift logic doesn't
// already handle.
//
// Reports whether a correction was actually made (false if segmentID
// wasn't found, e.g. a concurrent realignment already ran).
func (r *StoredRarReader) realignSpan(segmentID int64, actual SegmentSpan) bool {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	state := r.state.Load()
	index := -1
	for i := range state.spans {
		if state.spans[i].SegmentID == segmentID {
			index = i
			break
		}
	}
	if index < 0 {
		return false
	}
	old := state.spans[index]
	available := actual.End - actual.Start - old.SegmentByteStart
	if available < 0 {
		available = 0
	}
	newLen := available
	if old.DecodedStart == actual.Start && old.End-old.Start == newLen {
		return false // nothing to correct; avoid an infinite retry loop
	}
	spans := append([]SegmentSpan(nil), state.spans...)
	spans[index].DecodedStart = actual.Start
	spans[index].End = old.Start + newLen
	delta := spans[index].End - old.End
	if delta != 0 {
		for i := index + 1; i < len(spans); i++ {
			spans[i].Start += delta
			spans[i].End += delta
		}
	}
	size := spans[len(spans)-1].End
	r.state.Store(&storedRarSpanState{
		spans:       spans,
		size:        size,
		version:     state.version + 1,
		layoutValid: validateStoredRarSpans(spans, size) == nil,
	})
	return true
}

// findStoredRarSpan resolves one virtual offset without constructing ranges
// for the rest of the request. spans belongs to an immutable state version.
func findStoredRarSpan(spans []SegmentSpan, offset int64) (SegmentSpan, error) {
	i := sort.Search(len(spans), func(i int) bool { return spans[i].End > offset })
	if i < len(spans) && spans[i].Start <= offset {
		return spans[i], nil
	}
	return SegmentSpan{}, ErrRangeOutsideFile
}

// StartSession registers this reader with its ReadAheadManager under
// sessionID, enabling read-ahead prefetch for the stream. A no-op if the
// reader has no manager or its fetcher doesn't support prioritized fetches.
func (r *StoredRarReader) StartSession(sessionID string) {
	if r == nil || r.manager == nil {
		return
	}
	fetcher, ok := r.fetcher.(PrioritySegmentFetcher)
	if !ok {
		return
	}
	state := r.state.Load()
	r.manager.Register(sessionID, state.spans, fetcher)
}

// NotifyRead reports the current read position for sessionID to the
// ReadAheadManager, which uses it to schedule the next prefetch window.
func (r *StoredRarReader) NotifyRead(sessionID string, offset int64) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.NotifyRead(sessionID, offset)
}

// Seek cancels any in-flight read-ahead window for sessionID without
// scheduling a new one at offset; the next NotifyRead (from the interactive
// read that follows a seek) schedules read-ahead from the correct position.
func (r *StoredRarReader) Seek(sessionID string, offset int64) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.Seek(sessionID, offset)
}

// StopSession ends the read-ahead session for sessionID and cancels any
// in-flight prefetch for it.
func (r *StoredRarReader) StopSession(sessionID string) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.Stop(sessionID)
}

// RegisterMeta attaches display metadata to the session's read-ahead entry.
func (r *StoredRarReader) RegisterMeta(sessionID string, meta SessionMeta) {
	if r == nil || r.manager == nil {
		return
	}
	r.manager.RegisterMeta(sessionID, meta)
}

// validateStoredRarSpans reports whether spans form a valid layout for a file
// of the given size: sorted, contiguous from 0, with no zero-or-negative
// length span, and collectively covering exactly [0, size). Validation runs
// when each immutable state version is created; ReadAt checks the stored
// verdict without rescanning the complete table.
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
