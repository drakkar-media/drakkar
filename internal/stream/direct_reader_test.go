package stream

import (
	"context"
	"sync"
	"testing"
)

type fetcherStub struct{}

func (fetcherStub) FetchRange(ctx context.Context, segment SegmentRange) ([]byte, error) {
	size := int(segment.RangeEnd - segment.RangeStart)
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte('A' + int(segment.SegmentID) - 1)
	}
	return buf, nil
}

type awareFetcherStub struct {
	data map[int64][]byte
	info map[int64]SegmentSpan
}

func (f awareFetcherStub) FetchRange(ctx context.Context, segment SegmentRange) ([]byte, error) {
	block, _, err := f.FetchRangeInfo(ctx, segment)
	return block, err
}

func (f awareFetcherStub) FetchRangeInfo(ctx context.Context, segment SegmentRange) ([]byte, SegmentSpan, error) {
	full := f.data[segment.SegmentID]
	actual := f.info[segment.SegmentID]
	start := int(segment.RangeStart - actual.Start)
	end := int(segment.RangeEnd - actual.Start)
	if start < 0 {
		start = 0
	}
	if end > len(full) {
		end = len(full)
	}
	out := make([]byte, end-start)
	copy(out, full[start:end])
	return out, actual, nil
}

// concurrentRealignFetcher simulates a segment whose real yEnc size differs
// from its estimate on the FIRST fetch of a given segment (triggering
// realignSpans), and matches exactly thereafter -- modeling the live
// calibration-correction pattern seen in production logs
// ("calibrate: corrected segment offsets"). Used to exercise ReadAt/
// realignSpans running concurrently on the same reader instance.
type concurrentRealignFetcher struct {
	mu    sync.Mutex
	seen  map[int64]bool
	data  map[int64][]byte
	first map[int64]SegmentSpan
	real  map[int64]SegmentSpan
}

func (f *concurrentRealignFetcher) FetchRange(ctx context.Context, segment SegmentRange) ([]byte, error) {
	block, _, err := f.FetchRangeInfo(ctx, segment)
	return block, err
}

func (f *concurrentRealignFetcher) FetchRangeInfo(ctx context.Context, segment SegmentRange) ([]byte, SegmentSpan, error) {
	f.mu.Lock()
	alreadySeen := f.seen[segment.SegmentID]
	f.seen[segment.SegmentID] = true
	f.mu.Unlock()

	actual := f.real[segment.SegmentID]
	if !alreadySeen {
		actual = f.first[segment.SegmentID]
	}
	full := f.data[segment.SegmentID]
	start := int(segment.RangeStart - actual.Start)
	end := int(segment.RangeEnd - actual.Start)
	if start < 0 {
		start = 0
	}
	if end > len(full) {
		end = len(full)
	}
	if start > end {
		start = end
	}
	out := make([]byte, end-start)
	copy(out, full[start:end])
	return out, actual, nil
}

// TestDirectNzbReaderConcurrentReadAtDuringRealign guards a real production
// incident (2026-07-25): intermittent video decode corruption during
// playback. Root cause: ReadAt read r.size exactly once, unguarded, at the
// top of the call and never refreshed it, unlike StoredRarReader.ReadAt
// (which re-snapshots size/spans every loop iteration). Two concurrent
// ReadAt calls on the same reader instance (e.g. Plex issuing overlapping
// Range requests) racing a mid-flight realignSpans correction could use a
// stale size/length bound. Run with -race: this must not report a data race
// on r.size, and every concurrent read must still return exactly the
// expected bytes for its own offset.
func TestDirectNzbReaderConcurrentReadAtDuringRealign(t *testing.T) {
	fetcher := &concurrentRealignFetcher{
		seen: map[int64]bool{},
		data: map[int64][]byte{
			1: []byte("AAAAAAAAAA"),
			2: []byte("BBBBBBBBBB"),
			3: []byte("CCCCCCCCCC"),
		},
		// Estimated boundaries (what the reader is constructed with).
		first: map[int64]SegmentSpan{
			1: {SegmentID: 1, Start: 0, End: 10},
			2: {SegmentID: 2, Start: 10, End: 20},
			3: {SegmentID: 3, Start: 20, End: 30},
		},
		// "Real" yEnc-measured boundaries, discovered on first fetch of each
		// segment -- shifted by +2 from segment 2 onward, same shape as a
		// live calibration correction.
		real: map[int64]SegmentSpan{
			1: {SegmentID: 1, Start: 0, End: 10},
			2: {SegmentID: 2, Start: 10, End: 22},
			3: {SegmentID: 3, Start: 22, End: 32},
		},
	}
	reader := NewDirectNzbReader("test.mkv", 30, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 10},
		{SegmentID: 2, Start: 10, End: 20},
		{SegmentID: 3, Start: 20, End: 30},
	}, fetcher, nil)

	// Offsets chosen to fall unambiguously within one content segment under
	// BOTH the estimated and the realigned boundaries (avoiding the shifted
	// segment-2/segment-3 boundary itself at 20-22, which moves under
	// realignment), so the expected byte is well-defined regardless of
	// which racing goroutine's realignment view wins.
	type probe struct {
		offset int64
		want   byte
	}
	probes := []probe{{2, 'A'}, {12, 'B'}, {25, 'C'}}
	const repeats = 20

	var wg sync.WaitGroup
	results := make([][]byte, len(probes)*repeats)
	errs := make([]error, len(probes)*repeats)
	for r := 0; r < repeats; r++ {
		for i, p := range probes {
			idx := r*len(probes) + i
			wg.Add(1)
			go func(idx int, offset int64) {
				defer wg.Done()
				buf := make([]byte, 3)
				n, err := reader.ReadAt(context.Background(), buf, offset)
				results[idx] = buf[:n]
				errs[idx] = err
			}(idx, p.offset)
		}
	}
	wg.Wait()

	for r := 0; r < repeats; r++ {
		for i, p := range probes {
			idx := r*len(probes) + i
			if errs[idx] != nil {
				t.Fatalf("offset %d: unexpected error %v", p.offset, errs[idx])
			}
			for _, b := range results[idx] {
				if b != p.want {
					t.Fatalf("offset %d: got %q, expected all bytes %q (wrong-offset data landed in this read -- corruption)", p.offset, results[idx], string(p.want))
				}
			}
		}
	}
}

func TestDirectNzbReaderReadAt(t *testing.T) {
	reader := NewDirectNzbReader("Dune.mkv", 300, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 100},
		{SegmentID: 2, Start: 100, End: 200},
		{SegmentID: 3, Start: 200, End: 300},
	}, fetcherStub{}, nil)
	buf := make([]byte, 120)
	n, err := reader.ReadAt(context.Background(), buf, 90)
	if err != nil {
		t.Fatal(err)
	}
	if n != 120 {
		t.Fatalf("expected 120 bytes, got %d", n)
	}
	if buf[0] != 'A' || buf[15] != 'B' || buf[110] != 'C' {
		t.Fatalf("unexpected segment stitch %q", string(buf[:20]))
	}
}

func TestDirectNzbReaderRealignsEstimatedBoundaries(t *testing.T) {
	reader := NewDirectNzbReader("test.mkv", 22, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 9},
		{SegmentID: 2, Start: 9, End: 18},
		{SegmentID: 3, Start: 18, End: 22},
	}, awareFetcherStub{
		data: map[int64][]byte{
			1: []byte("AAAAAAAAAA"),
			2: []byte("BBBBBBBBBB"),
			3: []byte("CC"),
		},
		info: map[int64]SegmentSpan{
			1: {SegmentID: 1, Start: 0, End: 10},
			2: {SegmentID: 2, Start: 10, End: 20},
			3: {SegmentID: 3, Start: 20, End: 22},
		},
	}, nil)

	buf := make([]byte, 12)
	n, err := reader.ReadAt(context.Background(), buf, 8)
	if err != nil {
		t.Fatal(err)
	}
	if n != 12 {
		t.Fatalf("expected 12 bytes, got %d", n)
	}
	if string(buf) != "AABBBBBBBBBB" {
		t.Fatalf("unexpected data %q", string(buf))
	}
}
