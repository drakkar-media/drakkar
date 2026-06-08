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
	for _, span := range ranges {
		block, err := r.fetcher.FetchRange(ctx, span)
		if err != nil {
			return written, err
		}
		expected := int(span.RangeEnd - span.RangeStart)
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
