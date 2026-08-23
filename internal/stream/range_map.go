package stream

import (
	"errors"
	"sort"
)

// ErrRangeOutsideFile is returned when a requested byte range cannot be
// fully satisfied by the given spans — either it falls outside [0, size), or
// it lands in a gap the spans don't cover contiguously.
var ErrRangeOutsideFile = errors.New("range outside file")

// SegmentRange is a concrete, boundary-split slice of a byte-range request:
// RangeStart/RangeEnd are the actual bytes needed from this segment, while
// SegmentStart/SegmentEnd and the decoded-* fields describe the segment's
// full span for context. ResolveRange produces these from a []SegmentSpan.
type SegmentRange struct {
	SegmentID        int64
	MessageID        string
	RangeStart       int64
	RangeEnd         int64
	SegmentStart     int64
	SegmentEnd       int64
	DecodedStart     int64 // decoded_start_offset of the NZB segment (stored_rar only)
	SegmentByteStart int64 // byte within decoded segment where this span's content starts
}

// SegmentSpan describes one NZB segment's placement within a virtual file's
// byte space: [Start, End) is the segment's contiguous slice of the VF, in
// VF-relative bytes. Readers keep a []SegmentSpan sorted and, in steady
// state, contiguous (each span's Start equal to the previous span's End).
// DirectNzbReader realigns its guarded table in place; StoredRarReader
// publishes immutable corrected copies. Both preserve contiguity by shifting
// every later span's Start/End by the same delta.
type SegmentSpan struct {
	SegmentID        int64
	MessageID        string
	Start            int64
	End              int64
	DecodedStart     int64 // decoded_start_offset of the NZB segment (stored_rar only)
	SegmentByteStart int64 // byte offset within decoded segment at span.Start (stored_rar only)
	// EntryTruncated is true when this span's End was cut short by the
	// archive entry's own declared length (archive_ranges.length_bytes),
	// not because the underlying NZB segment ran out of decoded data
	// (stored_rar only; always false for DirectNzbReader spans, which have
	// no such archive-level boundary). A non-final RAR volume's last
	// segment routinely has real bytes past this point -- trailing RAR
	// container metadata (e.g. a "QO" Quick Open service block) that
	// belongs to the archive, not the video -- so a live read confirming
	// "more data exists here than this span claims" must never be treated
	// as an under-estimate to self-heal from.
	EntryTruncated bool
}

// ResolveRange splits the byte range [offset, offset+length) into one
// SegmentRange per span it overlaps, in ascending order.
//
// It requires spans to be sorted and contiguous over the requested range: any
// gap between two consecutive resolved ranges, or a requested range that
// isn't fully covered from offset through offset+length, is treated as a
// layout error rather than silently returning a partial or discontiguous
// result.
//
// Errors:
//   - ErrRangeOutsideFile: offset or length is negative, the requested range
//     is not fully covered by spans, or a gap exists between two spans that
//     should have been contiguous.
func ResolveRange(spans []SegmentSpan, offset, length int64) ([]SegmentRange, error) {
	return resolveRange(spans, offset, length, 0)
}

// resolveRange returns a contiguous range plan using binary search to enter
// the span table. maxRanges > 0 intentionally returns a covered prefix after
// that many segments; read-ahead uses this to enforce its article limit
// without constructing and then discarding the rest of a large window.
func resolveRange(spans []SegmentSpan, offset, length int64, maxRanges int) ([]SegmentRange, error) {
	if length < 0 || offset < 0 {
		return nil, ErrRangeOutsideFile
	}
	if length == 0 {
		return []SegmentRange{}, nil
	}
	requestEnd := offset + length
	if requestEnd < offset {
		return nil, ErrRangeOutsideFile
	}
	first := sort.Search(len(spans), func(i int) bool { return spans[i].End > offset })
	if first == len(spans) || spans[first].Start > offset {
		return nil, ErrRangeOutsideFile
	}
	last := sort.Search(len(spans), func(i int) bool { return spans[i].End >= requestEnd })
	capacity := len(spans) - first
	if last >= first && last < len(spans) {
		capacity = last - first + 1
	}
	if capacity <= 0 {
		return nil, ErrRangeOutsideFile
	}
	if maxRanges > 0 && capacity > maxRanges {
		capacity = maxRanges
	}
	out := make([]SegmentRange, 0, capacity)
	cursor := offset
	for i := first; i < len(spans) && cursor < requestEnd; i++ {
		span := spans[i]
		if span.End <= span.Start || (i == first && span.Start > cursor) || (i > first && span.Start != cursor) {
			return nil, ErrRangeOutsideFile
		}
		end := min(requestEnd, span.End)
		if end <= cursor {
			return nil, ErrRangeOutsideFile
		}
		out = append(out, SegmentRange{
			SegmentID:        span.SegmentID,
			MessageID:        span.MessageID,
			RangeStart:       cursor,
			RangeEnd:         end,
			SegmentStart:     span.Start,
			SegmentEnd:       span.End,
			DecodedStart:     span.DecodedStart,
			SegmentByteStart: span.SegmentByteStart,
		})
		cursor = end
		if maxRanges > 0 && len(out) == maxRanges {
			break
		}
	}
	if cursor != requestEnd && !(maxRanges > 0 && len(out) == maxRanges) {
		return nil, ErrRangeOutsideFile
	}
	return out, nil
}
