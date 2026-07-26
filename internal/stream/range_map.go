package stream

import "errors"

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
// state, contiguous (each span's Start equal to the previous span's End);
// DirectNzbReader.realignSpans and StoredRarReader.realignSpan are the only
// code paths permitted to mutate a span in place, and both preserve
// contiguity by shifting every later span's Start/End by the same delta.
type SegmentSpan struct {
	SegmentID        int64
	MessageID        string
	Start            int64
	End              int64
	DecodedStart     int64 // decoded_start_offset of the NZB segment (stored_rar only)
	SegmentByteStart int64 // byte offset within decoded segment at span.Start (stored_rar only)
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
	if length < 0 || offset < 0 {
		return nil, ErrRangeOutsideFile
	}
	if length == 0 {
		return []SegmentRange{}, nil
	}
	requestEnd := offset + length
	var out []SegmentRange
	for _, span := range spans {
		if requestEnd <= span.Start {
			break
		}
		if offset >= span.End {
			continue
		}
		start := max(offset, span.Start)
		end := min(requestEnd, span.End)
		if start >= end {
			continue
		}
		// Every span appended after the first must pick up exactly where the
		// previous one left off. Found during the 2026-07-25 corruption
		// audit as an unguarded gap: nothing previously checked this, only
		// that the LAST returned range reached requestEnd -- a gap between
		// two middle spans (which shouldn't occur by construction today,
		// since realignSpans/realignSpan always shift every later span by
		// the same delta to preserve contiguity, but nothing enforced that
		// invariant here) would have silently dropped/misaligned the bytes
		// in between instead of surfacing an error, exactly the class of
		// bug already fixed twice this session via other mechanisms.
		if len(out) > 0 && start != out[len(out)-1].RangeEnd {
			return nil, ErrRangeOutsideFile
		}
		out = append(out, SegmentRange{
			SegmentID:        span.SegmentID,
			MessageID:        span.MessageID,
			RangeStart:       start,
			RangeEnd:         end,
			SegmentStart:     span.Start,
			SegmentEnd:       span.End,
			DecodedStart:     span.DecodedStart,
			SegmentByteStart: span.SegmentByteStart,
		})
	}
	if len(out) == 0 {
		return nil, ErrRangeOutsideFile
	}
	last := out[len(out)-1]
	if last.RangeEnd != requestEnd {
		return nil, ErrRangeOutsideFile
	}
	return out, nil
}
