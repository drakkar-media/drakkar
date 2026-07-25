package stream

import "errors"

var ErrRangeOutsideFile = errors.New("range outside file")

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

type SegmentSpan struct {
	SegmentID        int64
	MessageID        string
	Start            int64
	End              int64
	DecodedStart     int64 // decoded_start_offset of the NZB segment (stored_rar only)
	SegmentByteStart int64 // byte offset within decoded segment at span.Start (stored_rar only)
}

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
