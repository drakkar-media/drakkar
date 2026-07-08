package stream

import (
	"context"
	"errors"
	"io"
)

var ErrStoredRarLayoutInvalid = errors.New("stored_rar layout invalid")

type StoredRarReader struct {
	name    string
	size    int64
	spans   []SegmentSpan
	fetcher SegmentFetcher
	manager *ReadAheadManager
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
	return r.size
}

func (r *StoredRarReader) ReadAt(ctx context.Context, dst []byte, offset int64) (int, error) {
	if err := validateStoredRarSpans(r.spans, r.size); err != nil {
		return 0, err
	}
	if offset >= r.size {
		return 0, io.EOF
	}
	length := int64(len(dst))
	if offset+length > r.size {
		length = r.size - offset
	}
	ranges, err := ResolveRange(r.spans, offset, length)
	if err != nil {
		return 0, err
	}
	written := 0
	for _, rng := range ranges {
		// Translate VF byte positions to decoded-segment byte positions.
		// vfr.range_start/end are in VF space; the NNTP fetcher expects archive
		// (decoded) byte positions. The offset into the decoded segment is:
		//   segment_byte_start + (vf_pos - span_vf_start)
		// where segment_byte_start accounts for the RAR header that precedes the
		// embedded file in the first segment of each volume.
		offset := rng.SegmentByteStart + (rng.RangeStart - rng.SegmentStart)
		length := rng.RangeEnd - rng.RangeStart
		adj := SegmentRange{
			SegmentID:    rng.SegmentID,
			MessageID:    rng.MessageID,
			RangeStart:   rng.DecodedStart + offset,
			RangeEnd:     rng.DecodedStart + offset + length,
			SegmentStart: rng.DecodedStart,
			SegmentEnd:   rng.DecodedStart + (rng.SegmentEnd - rng.SegmentStart) + rng.SegmentByteStart,
		}
		block, err := r.fetcher.FetchRange(ctx, adj)
		expected := int(length)
		if err == nil && len(block) < expected {
			// A short read here doesn't necessarily mean the archive layout
			// is wrong — it can also be a transient network/segment hiccup,
			// which DirectNzbReader tolerates via its realign/retry loop.
			// Retry once before treating it as fatal.
			block, err = r.fetcher.FetchRange(ctx, adj)
		}
		if err != nil {
			return written, err
		}
		if len(block) < expected {
			return written, errors.New("short fetch")
		}
		copy(dst[written:written+expected], block[:expected])
		written += expected
	}
	if int64(written) < int64(len(dst)) {
		return written, io.EOF
	}
	return written, nil
}

func (r *StoredRarReader) StartSession(sessionID string) {
	if r == nil || r.manager == nil {
		return
	}
	fetcher, ok := r.fetcher.(PrioritySegmentFetcher)
	if !ok {
		return
	}
	r.manager.Register(sessionID, r.spans, fetcher)
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
