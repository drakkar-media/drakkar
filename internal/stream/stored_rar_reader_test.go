package stream

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestStoredRarReaderReadAt(t *testing.T) {
	reader := NewStoredRarReader("Movie.mkv", 200, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 50},
		{SegmentID: 2, Start: 50, End: 150},
		{SegmentID: 3, Start: 150, End: 200},
	}, fetcherStub{}, nil)
	buf := make([]byte, 80)
	n, err := reader.ReadAt(context.Background(), buf, 40)
	if err != nil {
		t.Fatal(err)
	}
	if n != 80 {
		t.Fatalf("expected 80 bytes, got %d", n)
	}
	if buf[0] != 'A' || buf[15] != 'B' || buf[70] != 'B' {
		t.Fatalf("unexpected segment stitch %q", string(buf[:20]))
	}
}

func TestStoredRarReaderRejectsGap(t *testing.T) {
	reader := NewStoredRarReader("Movie.mkv", 120, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 50},
		{SegmentID: 2, Start: 60, End: 120},
	}, fetcherStub{}, nil)
	_, err := reader.ReadAt(context.Background(), make([]byte, 16), 0)
	if !errors.Is(err, ErrStoredRarLayoutInvalid) {
		t.Fatalf("expected invalid layout, got %v", err)
	}
}

func TestStoredRarReaderRejectsWrongSizeCoverage(t *testing.T) {
	reader := NewStoredRarReader("Movie.mkv", 140, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 50},
		{SegmentID: 2, Start: 50, End: 120},
	}, fetcherStub{}, nil)
	_, err := reader.ReadAt(context.Background(), make([]byte, 16), 0)
	if !errors.Is(err, ErrStoredRarLayoutInvalid) {
		t.Fatalf("expected invalid layout, got %v", err)
	}
}

// TestStoredRarReaderRealignsLastSegmentEstimate guards the production fix:
// a calibrated decoded_segment_size/last_decoded_size estimate for one NNTP
// segment that overshoots its true decoded size previously caused a hard
// "short fetch" error for any read touching that span -- confirmed live to
// hit almost exclusively the last segment of the last volume, since
// truncateSpans only reconciles the aggregate total against
// virtual_files.size_bytes, not each segment's own real boundaries. Real
// players probe near true EOF for trailing container metadata (MP4 moov,
// MKV cues), so this silently broke "video: none, audio: none" for every
// affected file even though the vast majority of the stream served fine.
//
// Segment 2 is declared as 9 bytes (VF span 10..19, matching
// virtual_files.size_bytes=19) but its real decoded content is only 8
// bytes -- exactly the shape of the confirmed real-world bug.
func TestStoredRarReaderRealignsLastSegmentEstimate(t *testing.T) {
	reader := NewStoredRarReader("Movie.mkv", 19, []SegmentSpan{
		{SegmentID: 1, MessageID: "<seg1>", Start: 0, End: 10, DecodedStart: 0, SegmentByteStart: 0},
		{SegmentID: 2, MessageID: "<seg2>", Start: 10, End: 19, DecodedStart: 10, SegmentByteStart: 0},
	}, awareFetcherStub{
		data: map[int64][]byte{
			1: []byte("AAAAAAAAAA"), // 10 bytes -- matches the estimate exactly
			2: []byte("BBBBBBBB"),   // 8 real bytes, short of the estimated 9-byte span
		},
		info: map[int64]SegmentSpan{
			1: {SegmentID: 1, MessageID: "<seg1>", Start: 0, End: 10},
			2: {SegmentID: 2, MessageID: "<seg2>", Start: 10, End: 18},
		},
	}, nil)

	buf := make([]byte, 9)
	n, err := reader.ReadAt(context.Background(), buf, 10)
	if err != io.EOF {
		t.Fatalf("expected io.EOF once the corrected size is exhausted, got %v", err)
	}
	if n != 8 {
		t.Fatalf("expected 8 real bytes delivered, got %d", n)
	}
	if string(buf[:8]) != "BBBBBBBB" {
		t.Fatalf("unexpected data %q", string(buf[:8]))
	}
	if got := reader.Size(); got != 18 {
		t.Fatalf("expected reader.Size() corrected to 18, got %d", got)
	}

	// A second read after the correction must see the corrected size and
	// boundaries directly, without needing to rediscover them.
	buf2 := make([]byte, 8)
	n2, err2 := reader.ReadAt(context.Background(), buf2, 10)
	if err2 != nil {
		t.Fatalf("unexpected error on re-read after correction: %v", err2)
	}
	if n2 != 8 || string(buf2) != "BBBBBBBB" {
		t.Fatalf("unexpected re-read result n=%d data=%q", n2, string(buf2))
	}
}

// TestStoredRarReaderRealignsUnderEstimatedSegment guards the production
// fix for the OTHER direction of the same estimate-vs-reality gap
// TestStoredRarReaderRealignsLastSegmentEstimate covers: a segment whose
// real decoded content is LARGER than the estimate used to build its span,
// not smaller. Confirmed live 2026-08-20 (a 2160p HEVC release embedded in
// a stored RAR volume): realignSpan used to clamp corrections to
// shrink-only, so an under-estimated segment
// kept its old, too-short VF-space length -- reads past that point resolved
// against the NEXT segment's own DecodedStart/SegmentByteStart instead of
// the true remaining tail of the current one, splicing in bytes from the
// wrong segment at that exact position (decoded as a block of corrupted
// pixels, severe enough to desync playback until a fresh seek recovered
// it).
//
// Segment 1 is declared as 10 bytes (VF span 0..10) but its real decoded
// content is 12 bytes -- realignSpan must grow the span to 0..12 and shift
// segment 2 forward to start at 12 (not the old estimate of 10), so a read
// spanning the original boundary returns segment 1's true tail bytes
// followed by segment 2's real content, not a premature splice.
func TestStoredRarReaderRealignsUnderEstimatedSegment(t *testing.T) {
	reader := NewStoredRarReader("Movie.mkv", 20, []SegmentSpan{
		{SegmentID: 1, MessageID: "<seg1>", Start: 0, End: 10, DecodedStart: 0, SegmentByteStart: 0},
		{SegmentID: 2, MessageID: "<seg2>", Start: 10, End: 20, DecodedStart: 10, SegmentByteStart: 0},
	}, awareFetcherStub{
		data: map[int64][]byte{
			1: []byte("AAAAAAAAAAAA"), // 12 real bytes, more than the estimated 10-byte span
			2: []byte("BBBBBBBBBB"),   // 10 bytes -- matches its own estimate exactly
		},
		info: map[int64]SegmentSpan{
			1: {SegmentID: 1, MessageID: "<seg1>", Start: 0, End: 12},
			2: {SegmentID: 2, MessageID: "<seg2>", Start: 10, End: 20},
		},
	}, nil)

	// Spans the original (wrong) boundary at VF offset 10: bytes 8-11 are
	// segment 1's true tail (all 'A's, including the 2 bytes past the old
	// estimate), bytes 12-13 are segment 2's real content starting at its
	// corrected offset.
	buf := make([]byte, 6)
	n, err := reader.ReadAt(context.Background(), buf, 8)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 6 {
		t.Fatalf("expected 6 bytes, got %d", n)
	}
	if got := string(buf); got != "AAAABB" {
		t.Fatalf("expected segment 1's grown tail spliced correctly with segment 2's real start, got %q", got)
	}
	if got := reader.Size(); got != 22 {
		t.Fatalf("expected reader.Size() corrected to 22 (segment 1 grew by 2), got %d", got)
	}
}

// TestStoredRarReaderStateRemainsStableAfterRealign guards the immutable
// copy-on-write state contract: a state obtained before realignment must
// retain its original backing data after the corrected version is published.
func TestStoredRarReaderStateRemainsStableAfterRealign(t *testing.T) {
	reader := NewStoredRarReader("test.mkv", 20, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 10, DecodedStart: 0},
		{SegmentID: 2, Start: 10, End: 20, DecodedStart: 10},
	}, fetcherStub{}, nil)

	beforeState := reader.state.Load()
	before := beforeState.spans
	beforeSize := beforeState.size
	beforeSpan2 := before[1]

	if !reader.realignSpan(2, SegmentSpan{SegmentID: 2, Start: 10, End: 18}) {
		t.Fatal("expected realignSpan to report a correction was made")
	}

	if before[1] != beforeSpan2 {
		t.Fatalf("state changed after realignment: got %+v, want unchanged %+v", before[1], beforeSpan2)
	}
	if beforeSize != 20 {
		t.Fatalf("unexpected pre-realign size %d", beforeSize)
	}

	afterState := reader.state.Load()
	after := afterState.spans
	afterSize := afterState.size
	if after[1].End != 18 {
		t.Fatalf("expected a FRESH snapshot to see the corrected span, got End=%d", after[1].End)
	}
	if afterSize != 18 {
		t.Fatalf("expected a fresh snapshot to see the corrected size 18, got %d", afterSize)
	}
}

type blockingStoredRarFetcher struct {
	started chan struct{}
	release chan struct{}
}

func (f *blockingStoredRarFetcher) FetchRange(_ context.Context, segment SegmentRange) ([]byte, error) {
	if segment.SegmentID == 2 {
		close(f.started)
		<-f.release
	}
	return []byte{byte('A' + segment.SegmentID - 1)}, nil
}

func TestStoredRarReaderRetriesFetchFromSupersededState(t *testing.T) {
	fetcher := &blockingStoredRarFetcher{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	reader := NewStoredRarReader("test.mkv", 30, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 10, DecodedStart: 0},
		{SegmentID: 2, Start: 10, End: 20, DecodedStart: 10},
		{SegmentID: 3, Start: 20, End: 30, DecodedStart: 20},
	}, fetcher, nil)

	type readResult struct {
		value byte
		n     int
		err   error
	}
	result := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 1)
		n, err := reader.ReadAt(context.Background(), buf, 15)
		result <- readResult{value: buf[0], n: n, err: err}
	}()

	select {
	case <-fetcher.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stale-state fetch")
	}
	if !reader.realignSpan(1, SegmentSpan{SegmentID: 1, Start: 0, End: 5}) {
		t.Fatal("expected first span to shrink")
	}
	close(fetcher.release)

	var got readResult
	select {
	case got = <-result:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retried read")
	}
	if got.err != nil || got.n != 1 {
		t.Fatalf("ReadAt = (%d, %v), want (1, nil)", got.n, got.err)
	}
	if got.value != 'C' {
		t.Fatalf("ReadAt returned %q from stale segment 2, want %q from corrected segment 3", got.value, byte('C'))
	}
}

// concurrentRealignRarFetcher mirrors concurrentRealignFetcher (direct_reader_test.go)
// for StoredRarReader's coordinate space (SegmentByteStart=0, DecodedStart=Start):
// segment 2's real decoded size is discovered to be 2 bytes shorter than its
// estimate on the FIRST fetch of that segment, matching the live "estimate
// overshoots true size" pattern this reader already self-heals from
// (TestStoredRarReaderRealignsLastSegmentEstimate above) -- but now exercised
// concurrently, from multiple goroutines racing realignSpan.
type concurrentRealignRarFetcher struct {
	mu    sync.Mutex
	seen  bool
	data  map[int64][]byte
	first map[int64]SegmentSpan
	real  map[int64]SegmentSpan
}

func (f *concurrentRealignRarFetcher) FetchRange(ctx context.Context, segment SegmentRange) ([]byte, error) {
	block, _, err := f.FetchRangeInfo(ctx, segment)
	return block, err
}

func (f *concurrentRealignRarFetcher) FetchRangeInfo(ctx context.Context, segment SegmentRange) ([]byte, SegmentSpan, error) {
	actual := f.real[segment.SegmentID]
	if segment.SegmentID == 2 {
		f.mu.Lock()
		alreadySeen := f.seen
		f.seen = true
		f.mu.Unlock()
		if !alreadySeen {
			actual = f.first[segment.SegmentID]
		}
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

// TestStoredRarReaderConcurrentReadAtDuringRealign guards a second, distinct
// production corruption bug found while investigating residual pixelation
// after the DirectNzbReader fix (v0.2.63). Run with -race: immutable state
// versions must prevent torn span data while concurrent reads and realignment
// overlap, and every read must return the correct segment's byte content.
func TestStoredRarReaderConcurrentReadAtDuringRealign(t *testing.T) {
	fetcher := &concurrentRealignRarFetcher{
		data: map[int64][]byte{
			1: []byte("AAAAAAAAAA"),
			2: []byte("BBBBBBBB"), // real: 8 bytes, short of the 10-byte estimate
			3: []byte("CCCCCCCCCC"),
		},
		first: map[int64]SegmentSpan{
			2: {SegmentID: 2, Start: 10, End: 20},
		},
		real: map[int64]SegmentSpan{
			1: {SegmentID: 1, Start: 0, End: 10},
			2: {SegmentID: 2, Start: 10, End: 18},
			3: {SegmentID: 3, Start: 20, End: 30},
		},
	}
	reader := NewStoredRarReader("test.mkv", 30, []SegmentSpan{
		{SegmentID: 1, Start: 0, End: 10, DecodedStart: 0},
		{SegmentID: 2, Start: 10, End: 20, DecodedStart: 10},
		{SegmentID: 3, Start: 20, End: 30, DecodedStart: 20},
	}, fetcher, nil)

	// Offsets chosen to resolve to the same, uniform-content segment
	// regardless of whether a racing goroutine's realignment (which shifts
	// segment 2's end, and everything after it, by up to -2) has applied
	// yet: offset 2 is deep inside segment 1 (untouched by any shift),
	// offset 12 is within segment 2's real (shortened) content on both
	// sides of the correction, and offset 26 is deep enough into segment 3
	// that a -2 shift still lands inside it either way.
	type probe struct {
		offset int64
		want   byte
	}
	probes := []probe{{2, 'A'}, {12, 'B'}, {26, 'C'}}
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
				buf := make([]byte, 2)
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
					t.Fatalf("offset %d: got %q, expected all bytes %q (wrong-segment data landed in this read -- corruption)", p.offset, results[idx], string(p.want))
				}
			}
		}
	}
}

type benchmarkStoredRarFetcher struct {
	block []byte
}

func (f benchmarkStoredRarFetcher) FetchRange(_ context.Context, segment SegmentRange) ([]byte, error) {
	return f.block[:segment.RangeEnd-segment.RangeStart], nil
}

func BenchmarkStoredRarReaderReadAt(b *testing.B) {
	const (
		spanCount = 2_800
		spanSize  = int64(750 << 10)
		readSize  = 64 << 10
	)
	spans := make([]SegmentSpan, spanCount)
	for i := range spans {
		spans[i] = SegmentSpan{
			SegmentID:    int64(i + 1),
			Start:        int64(i) * spanSize,
			End:          int64(i+1) * spanSize,
			DecodedStart: int64(i) * spanSize,
		}
	}
	reader := NewStoredRarReader(
		"benchmark.mkv",
		int64(spanCount)*spanSize,
		spans,
		benchmarkStoredRarFetcher{block: make([]byte, readSize)},
		nil,
	)
	dst := make([]byte, readSize)
	offset := int64(spanCount/2)*spanSize + 1_024

	b.ReportAllocs()
	b.SetBytes(readSize)
	b.ResetTimer()
	for range b.N {
		n, err := reader.ReadAt(context.Background(), dst, offset)
		if err != nil || n != len(dst) {
			b.Fatalf("ReadAt = (%d, %v), want (%d, nil)", n, err, len(dst))
		}
	}
}
