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
		start := max64(offset, span.Start)
		end := min64(requestEnd, span.End)
		if start >= end {
			continue
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

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
