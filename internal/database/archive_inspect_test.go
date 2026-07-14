package database

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bodgit/sevenzip"
	"github.com/drakkar-media/drakkar/internal/stream"
)

type fetcherStub struct {
	data []byte
	err  error
}

func (f fetcherStub) FetchRange(ctx context.Context, segment stream.SegmentRange) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]byte(nil), f.data[segment.RangeStart:segment.RangeEnd]...), nil
}

// TestReconcileStoreMethodSizeCorrectsImpossibleValue guards the fix for a
// confirmed real-world case: a multi-volume store-method (m0) RAR5 archive
// whose own header declared an unpacked size ~2.84x larger than the true
// total (verified byte-for-byte to be a genuine upstream packer defect, not
// a drakkar parsing bug). Since store method makes packed == unpacked a
// mathematical certainty, and content can never exceed what was actually
// posted across every volume, the declared size must be clamped to that
// hard ceiling.
func TestReconcileStoreMethodSizeCorrectsImpossibleValue(t *testing.T) {
	e := ImportedArchiveEntry{
		CompressionMethod: "m0",
		SizeBytes:         11_042_169_794,
		PackedSizeBytes:   209_714_896, // volume 1's own local header-parsed size
	}
	reconcileStoreMethodSize(&e, 3_892_423_326) // real total posted across all volumes
	if e.SizeBytes != 3_892_423_326 {
		t.Fatalf("expected SizeBytes clamped to the real total, got %d", e.SizeBytes)
	}
	if e.PackedSizeBytes != e.SizeBytes {
		t.Fatalf("expected PackedSizeBytes to match the corrected SizeBytes, got %d", e.PackedSizeBytes)
	}
}

// TestReconcileStoreMethodSizeLeavesPlausibleValueUnchanged guards against a
// false-positive correction: a normal, small disagreement (RAR header
// overhead subtracted from the total) must not get clobbered.
func TestReconcileStoreMethodSizeLeavesPlausibleValueUnchanged(t *testing.T) {
	e := ImportedArchiveEntry{
		CompressionMethod: "m0",
		SizeBytes:         4_690_284_791,
		PackedSizeBytes:   52_223_884, // volume 1's own local size, legitimately smaller
	}
	reconcileStoreMethodSize(&e, 4_690_352_514) // real total, comfortably larger than SizeBytes
	if e.SizeBytes != 4_690_284_791 {
		t.Fatalf("expected plausible SizeBytes left unchanged, got %d", e.SizeBytes)
	}
	if e.PackedSizeBytes != e.SizeBytes {
		t.Fatalf("expected PackedSizeBytes to adopt SizeBytes under store method, got %d", e.PackedSizeBytes)
	}
}

// TestReconcileStoreMethodSizeIgnoresNonStoreMethod guards against touching
// compressed archives, where packed != unpacked is expected and correct.
func TestReconcileStoreMethodSizeIgnoresNonStoreMethod(t *testing.T) {
	e := ImportedArchiveEntry{
		CompressionMethod: "m3",
		SizeBytes:         1_000_000_000,
		PackedSizeBytes:   500_000_000,
	}
	reconcileStoreMethodSize(&e, 600_000_000)
	if e.SizeBytes != 1_000_000_000 || e.PackedSizeBytes != 500_000_000 {
		t.Fatalf("expected non-store-method entry untouched, got %+v", e)
	}
}

func TestInspectImportedArchivesStoredRAR(t *testing.T) {
	raw := buildRAR4(false, false, 0x30, "Movie.mkv", 1024)
	files := []ImportedNZBFile{{
		FileName:      "Movie.part01.rar",
		FileSizeBytes: int64(len(raw)),
		Segments: []ImportedNZBSegment{{
			MessageID:          "<one@test>",
			DecodedStartOffset: 0,
			DecodedEndOffset:   int64(len(raw)),
		}},
	}}
	archives := inspectImportedArchives(context.Background(), []ImportedArchive{{
		Kind:   "rar",
		Status: "pending",
		Volumes: []ImportedArchiveVolume{
			{Path: "Movie.part01.rar", VolumeIndex: 0},
		},
	}}, files, fetcherStub{data: raw})
	if len(archives) != 1 {
		t.Fatalf("unexpected archives %+v", archives)
	}
	if archives[0].Status != "supported" || archives[0].RejectReason != "" {
		t.Fatalf("unexpected archive %+v", archives[0])
	}
	if len(archives[0].Entries) != 1 || archives[0].Entries[0].CompressionMethod != "m0" {
		t.Fatalf("unexpected entries %+v", archives[0].Entries)
	}
	if archives[0].Entries[0].PackedSizeBytes != 1024 || archives[0].Entries[0].VolumeIndex != 0 {
		t.Fatalf("unexpected entry source metadata %+v", archives[0].Entries[0])
	}
	if archives[0].Entries[0].ArchiveOffset <= 0 {
		t.Fatalf("expected positive archive offset, got %+v", archives[0].Entries[0])
	}
	if len(archives[0].Entries[0].Ranges) != 1 {
		t.Fatalf("unexpected ranges %+v", archives[0].Entries[0].Ranges)
	}
	if archives[0].Entries[0].Ranges[0].EntryOffset != 0 || archives[0].Entries[0].Ranges[0].LengthBytes != 1024 {
		t.Fatalf("unexpected first range %+v", archives[0].Entries[0].Ranges[0])
	}
}

func TestInspectImportedArchivesRejectsCompressedRAR(t *testing.T) {
	raw := buildRAR4(false, false, 0x33, "Movie.mkv", 1024)
	files := []ImportedNZBFile{{
		FileName:      "Movie.part01.rar",
		FileSizeBytes: int64(len(raw)),
		Segments: []ImportedNZBSegment{{
			MessageID:          "<one@test>",
			DecodedStartOffset: 0,
			DecodedEndOffset:   int64(len(raw)),
		}},
	}}
	archives := inspectImportedArchives(context.Background(), []ImportedArchive{{
		Kind: "rar",
		Volumes: []ImportedArchiveVolume{
			{Path: "Movie.part01.rar", VolumeIndex: 0},
		},
	}}, files, fetcherStub{data: raw})
	if archives[0].Status != "rejected" || archives[0].RejectReason != "archive_compression_unsupported" {
		t.Fatalf("unexpected archive %+v", archives[0])
	}
}

func TestInspectImportedArchivesRejectsInvalidHeaders(t *testing.T) {
	archives := inspectImportedArchives(context.Background(), []ImportedArchive{{
		Kind: "rar",
		Volumes: []ImportedArchiveVolume{
			{Path: "Movie.part01.rar", VolumeIndex: 0},
		},
	}}, []ImportedNZBFile{{
		FileName:      "Movie.part01.rar",
		FileSizeBytes: 16,
		Segments: []ImportedNZBSegment{{
			MessageID:          "<one@test>",
			DecodedStartOffset: 0,
			DecodedEndOffset:   16,
		}},
	}}, fetcherStub{data: []byte("not-rar-header!!")})
	if archives[0].Status != "rejected" || archives[0].RejectReason != "archive_headers_invalid" {
		t.Fatalf("unexpected archive %+v", archives[0])
	}
}

func TestInspectImportedArchivesPropagatesMissingArticleFailure(t *testing.T) {
	archives := inspectImportedArchives(context.Background(), []ImportedArchive{{
		Kind: "rar",
		Volumes: []ImportedArchiveVolume{
			{Path: "Movie.part01.rar", VolumeIndex: 0},
		},
	}}, []ImportedNZBFile{{
		FileName:      "Movie.part01.rar",
		FileSizeBytes: 1024,
		Segments: []ImportedNZBSegment{{
			MessageID:          "<one@test>",
			DecodedStartOffset: 0,
			DecodedEndOffset:   1024,
		}},
	}}, fetcherStub{err: errors.New("fetch decoded article <one@test>: Newshosting attempt 1: unexpected BODY status 430")})
	if archives[0].Status != "rejected" {
		t.Fatalf("expected rejected archive, got %+v", archives[0])
	}
	if !strings.Contains(archives[0].RejectReason, "nntp_article_unavailable") {
		t.Fatalf("expected missing-article reason, got %+v", archives[0])
	}
}

func TestInspectImportedArchivesUsesSegmentSizeWhenNZBFileSizeMetadataIsTooSmall(t *testing.T) {
	raw := buildRAR4(false, false, 0x30, "Movie.mkv", 1024)
	archives := inspectImportedArchives(context.Background(), []ImportedArchive{{
		Kind: "rar",
		Volumes: []ImportedArchiveVolume{
			{Path: "Movie.part01.rar", VolumeIndex: 0},
		},
	}}, []ImportedNZBFile{{
		FileName:      "Movie.part01.rar",
		FileSizeBytes: 128,
		Segments: []ImportedNZBSegment{{
			MessageID:          "<one@test>",
			DecodedStartOffset: 0,
			DecodedEndOffset:   int64(len(raw)),
		}},
	}}, fetcherStub{data: raw})
	if archives[0].Status != "supported" || archives[0].RejectReason != "" {
		t.Fatalf("unexpected archive %+v", archives[0])
	}
}

func TestInspectImportedArchivesRetriesLargerRARPrefix(t *testing.T) {
	raw := buildRAR4WithEntries([]rarFixtureEntry{
		{name: "proof01.nfo", method: 0x30, payloadSize: 300000},
		{name: "Movie.mkv", method: 0x30, payloadSize: 1024},
	})
	archives := inspectImportedArchives(context.Background(), []ImportedArchive{{
		Kind: "rar",
		Volumes: []ImportedArchiveVolume{
			{Path: "Movie.part01.rar", VolumeIndex: 0},
		},
	}}, []ImportedNZBFile{{
		FileName:      "Movie.part01.rar",
		FileSizeBytes: int64(len(raw)),
		Segments: []ImportedNZBSegment{{
			MessageID:          "<one@test>",
			DecodedStartOffset: 0,
			DecodedEndOffset:   int64(len(raw)),
		}},
	}}, fetcherStub{data: raw})
	if archives[0].Status != "supported" || archives[0].RejectReason != "" {
		t.Fatalf("unexpected archive %+v", archives[0])
	}
	if len(archives[0].Entries) == 0 {
		t.Fatalf("expected parsed entries, got %+v", archives[0])
	}
}

func TestInspectImportedArchivesLeavesPendingWithoutFetcher(t *testing.T) {
	archives := inspectImportedArchives(context.Background(), []ImportedArchive{{
		Kind: "rar",
		Volumes: []ImportedArchiveVolume{
			{Path: "Movie.part01.rar", VolumeIndex: 0},
		},
	}}, nil, nil)
	if archives[0].Status != "pending" {
		t.Fatalf("unexpected archive %+v", archives[0])
	}
}

func TestReadImportedFilePrefixShortFetch(t *testing.T) {
	file := ImportedNZBFile{
		FileName:      "Movie.part01.rar",
		FileSizeBytes: 8,
		Segments: []ImportedNZBSegment{{
			MessageID:          "<one@test>",
			DecodedStartOffset: 0,
			DecodedEndOffset:   8,
		}},
	}
	_, err := readImportedFilePrefix(context.Background(), file, 8, fetcherStub{err: errors.New("boom")})
	if err == nil {
		t.Fatal("expected fetch error")
	}
}

// fetchRangeInfoStub implements the FetchRangeInfo capability importedFileActualSize
// looks for, returning a fixed measured end offset regardless of the request.
type fetchRangeInfoStub struct {
	end int64
	err error
}

func (f fetchRangeInfoStub) FetchRange(ctx context.Context, segment stream.SegmentRange) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (f fetchRangeInfoStub) FetchRangeInfo(ctx context.Context, segment stream.SegmentRange) ([]byte, stream.SegmentSpan, error) {
	if f.err != nil {
		return nil, stream.SegmentSpan{}, f.err
	}
	return nil, stream.SegmentSpan{End: f.end}, nil
}

// TestImportedFileEffectiveSizePrefersRealMeasurementOverInflatedEstimate
// guards the stored_rar fix: a live-measured last-segment size must win
// outright over file.FileSizeBytes, not lose a max() comparison to it.
// Confirmed live in production against a real multi-volume RAR release: the
// real per-volume content (768000 x 68 = 52,224,000, matching the actual
// yEnc posting) was smaller than the volume's own FileSizeBytes estimate
// (52,297,799, a rougher pre-fetch guess) -- taking the max of the two
// silently kept the wrong, larger estimate on every volume, which fed
// assignArchiveRanges an inflated per-volume capacity and corrupted the
// whole file's byte layout downstream.
func TestImportedFileEffectiveSizePrefersRealMeasurementOverInflatedEstimate(t *testing.T) {
	file := ImportedNZBFile{
		FileName:      "part001.rar",
		FileSizeBytes: 52_297_799, // rough pre-fetch estimate: larger than the truth
		Segments: []ImportedNZBSegment{{
			MessageID:          "<one@test>",
			DecodedStartOffset: 0,
			DecodedEndOffset:   52_224_000,
		}},
	}
	got := importedFileEffectiveSize(context.Background(), file, fetchRangeInfoStub{end: 52_224_000})
	if got != 52_224_000 {
		t.Fatalf("importedFileEffectiveSize = %d, want the measured 52224000 (not the inflated FileSizeBytes estimate)", got)
	}
}

// TestImportedFileEffectiveSizeFallsBackWhenMeasurementUnavailable guards the
// other half of the fix: when no real measurement can be taken (fetcher
// doesn't support FetchRangeInfo, or the fetch fails), behavior must be
// unchanged from before -- the max of the segment-derived estimate and
// FileSizeBytes, exactly matching
// TestInspectImportedArchivesUsesSegmentSizeWhenNZBFileSizeMetadataIsTooSmall's
// expectations at the inspectImportedArchives layer.
func TestImportedFileEffectiveSizeFallsBackWhenMeasurementUnavailable(t *testing.T) {
	file := ImportedNZBFile{
		FileName:      "part001.rar",
		FileSizeBytes: 128, // too small vs. the real segment-derived estimate
		Segments: []ImportedNZBSegment{{
			MessageID:          "<one@test>",
			DecodedStartOffset: 0,
			DecodedEndOffset:   1024,
		}},
	}
	got := importedFileEffectiveSize(context.Background(), file, fetcherStub{data: make([]byte, 1024)})
	if got != 1024 {
		t.Fatalf("importedFileEffectiveSize = %d, want fallback max(segmentEnd=1024, FileSizeBytes=128) = 1024", got)
	}
}

func TestAssignArchiveRangesAcrossVolumes(t *testing.T) {
	entries := []ImportedArchiveEntry{{
		Path:            "Movie.mkv",
		PackedSizeBytes: 120,
		VolumeIndex:     0,
		ArchiveOffset:   80,
	}}
	assignArchiveRanges(entries, map[int]int64{
		0: 100,
		1: 150,
	}, nil)
	if len(entries[0].Ranges) != 2 {
		t.Fatalf("unexpected ranges %+v", entries[0].Ranges)
	}
	if entries[0].Ranges[0].LengthBytes != 20 || entries[0].Ranges[1].EntryOffset != 20 || entries[0].Ranges[1].LengthBytes != 100 {
		t.Fatalf("unexpected cross-volume mapping %+v", entries[0].Ranges)
	}
}

func TestAggregateRARVolumeEntriesAcrossParts(t *testing.T) {
	entries, err := aggregateRARVolumeEntries([]ImportedArchiveEntry{
		{
			Path:              "Movie.mkv",
			SizeBytes:         120,
			PackedSizeBytes:   20,
			CompressionMethod: "m0",
			VolumeIndex:       0,
			ArchiveOffset:     80,
		},
		{
			Path:              "Movie.mkv",
			SizeBytes:         120,
			PackedSizeBytes:   100,
			CompressionMethod: "m0",
			VolumeIndex:       1,
			ArchiveOffset:     0,
		},
	}, map[int]int64{
		0: 100,
		1: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %+v", entries)
	}
	entry := entries[0]
	if entry.PackedSizeBytes != 120 || entry.SizeBytes != 120 {
		t.Fatalf("unexpected entry sizes %+v", entry)
	}
	if len(entry.Ranges) != 2 {
		t.Fatalf("unexpected ranges %+v", entry.Ranges)
	}
	if entry.Ranges[0].EntryOffset != 0 || entry.Ranges[0].LengthBytes != 20 {
		t.Fatalf("unexpected first range %+v", entry.Ranges[0])
	}
	if entry.Ranges[1].EntryOffset != 20 || entry.Ranges[1].LengthBytes != 100 {
		t.Fatalf("unexpected second range %+v", entry.Ranges[1])
	}
}

func TestAggregateRARVolumeEntriesCorrectsImpossibleStoreMethodSize(t *testing.T) {
	entries, err := aggregateRARVolumeEntries([]ImportedArchiveEntry{
		{
			Path:              "Movie.mkv",
			SizeBytes:         1000, // corrupt header value, identical across every volume
			PackedSizeBytes:   100,
			CompressionMethod: "m0",
			VolumeIndex:       0,
			ArchiveOffset:     50,
		},
		{
			Path:              "Movie.mkv",
			SizeBytes:         1000,
			PackedSizeBytes:   100,
			CompressionMethod: "m0",
			VolumeIndex:       1,
			ArchiveOffset:     0,
		},
		{
			Path:              "Movie.mkv",
			SizeBytes:         1000,
			PackedSizeBytes:   100,
			CompressionMethod: "m0",
			VolumeIndex:       2,
			ArchiveOffset:     0,
		},
	}, map[int]int64{
		0: 150,
		1: 100,
		2: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %+v", entries)
	}
	entry := entries[0]
	if entry.PackedSizeBytes != 300 {
		t.Fatalf("expected packed size 300 (real accumulated total), got %+v", entry)
	}
	if entry.SizeBytes != 300 {
		t.Fatalf("expected declared size clamped to real accumulated total 300, got %+v", entry)
	}
	if len(entry.Ranges) != 3 {
		t.Fatalf("unexpected ranges %+v", entry.Ranges)
	}
}

// TestAggregateRARVolumeEntriesRaisesUndercountedStoreMethodSize guards
// against a real production bug found on "Lost S01E01" (8-volume store-method
// RAR): the header-declared SizeBytes (707874886) was 127 bytes *smaller*
// than the real, per-volume-calibrated PackedSizeBytes (707875013) -- the
// opposite direction from the "impossibly larger" Transformers case above.
// Since store method still guarantees packed == unpacked here, the fix must
// correct in both directions, not just clamp an oversized declared value
// down. Uncorrected, virtual_files.size_bytes was 127 bytes too small,
// so every stream of this file had its last 127 bytes of real video
// truncated by Content-Length.
func TestAggregateRARVolumeEntriesRaisesUndercountedStoreMethodSize(t *testing.T) {
	const declaredSize = 707874886 // header UnpackSize, identical on every volume
	entries := []ImportedArchiveEntry{
		{Path: "Lost.S01E01.mkv", SizeBytes: declaredSize, PackedSizeBytes: 92274577, CompressionMethod: "m0", VolumeIndex: 0, ArchiveOffset: 111},
	}
	volumeSizes := map[int]int64{0: 111 + 92274577}
	for i := 1; i <= 6; i++ {
		entries = append(entries, ImportedArchiveEntry{
			Path: "Lost.S01E01.mkv", SizeBytes: declaredSize, PackedSizeBytes: 92274449, CompressionMethod: "m0", VolumeIndex: i, ArchiveOffset: 112,
		})
		volumeSizes[i] = 112 + 92274449
	}
	entries = append(entries, ImportedArchiveEntry{
		Path: "Lost.S01E01.mkv", SizeBytes: declaredSize, PackedSizeBytes: 61953742, CompressionMethod: "m0", VolumeIndex: 7, ArchiveOffset: 112,
	})
	volumeSizes[7] = 112 + 61953742

	aggregated, err := aggregateRARVolumeEntries(entries, volumeSizes)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregated) != 1 {
		t.Fatalf("expected 1 entry, got %+v", aggregated)
	}
	entry := aggregated[0]
	const realTotal = 707875013
	if entry.PackedSizeBytes != realTotal {
		t.Fatalf("expected packed size %d (real accumulated total), got %+v", realTotal, entry)
	}
	if entry.SizeBytes != realTotal {
		t.Fatalf("expected undercounted declared size raised to the real accumulated total %d, got %+v", realTotal, entry)
	}
}

func TestHasCompleteArchiveMapping(t *testing.T) {
	if !hasCompleteArchiveMapping(ImportedArchiveEntry{
		PackedSizeBytes: 120,
		Ranges: []ImportedArchiveRange{
			{EntryOffset: 0, LengthBytes: 20},
			{EntryOffset: 20, LengthBytes: 100},
		},
	}) {
		t.Fatal("expected mapping to be complete")
	}
	if hasCompleteArchiveMapping(ImportedArchiveEntry{
		PackedSizeBytes: 120,
		Ranges: []ImportedArchiveRange{
			{EntryOffset: 0, LengthBytes: 20},
			{EntryOffset: 30, LengthBytes: 90},
		},
	}) {
		t.Fatal("expected mapping gap to be incomplete")
	}
}

func TestInspect7zEntriesStoredCopy(t *testing.T) {
	raw := loadSevenZipFixture(t, "t0.7z")
	files := []ImportedNZBFile{{
		FileName:      "Movie.7z",
		FileSizeBytes: int64(len(raw)),
		Segments: []ImportedNZBSegment{{
			MessageID:          "<one@test>",
			DecodedStartOffset: 0,
			DecodedEndOffset:   int64(len(raw)),
		}},
	}}
	fileByName := map[string]ImportedNZBFile{"Movie.7z": files[0]}
	readerAt, volumeSizes, totalSize, err := buildImportedArchiveReader(context.Background(), []ImportedArchiveVolume{{Path: "Movie.7z", VolumeIndex: 0}}, fileByName, fetcherStub{data: raw})
	if err != nil {
		t.Fatalf("buildImportedArchiveReader: %v", err)
	}
	reader, err := sevenzip.NewReader(readerAt, totalSize)
	if err != nil {
		t.Fatalf("sevenzip.NewReader: %v", err)
	}
	entries, err := inspect7zEntries(reader, volumeSizes)
	if err != nil {
		t.Fatalf("inspect7zEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected two entries, got %+v", entries)
	}
	if entries[0].CompressionMethod != "copy" || entries[1].CompressionMethod != "copy" {
		t.Fatalf("unexpected methods %+v", entries)
	}
	if entries[0].PackedSizeBytes != entries[0].SizeBytes || len(entries[0].Ranges) != 1 {
		t.Fatalf("unexpected first entry %+v", entries[0])
	}
	if entries[0].Ranges[0].EntryOffset != 0 || entries[0].Ranges[0].ArchiveOffset < 0 {
		t.Fatalf("unexpected first range %+v", entries[0].Ranges[0])
	}
}

func TestInspect7zEntriesRejectsCompressedArchive(t *testing.T) {
	raw := loadSevenZipFixture(t, "lzma.7z")
	files := []ImportedNZBFile{{
		FileName:      "Movie.7z",
		FileSizeBytes: int64(len(raw)),
		Segments: []ImportedNZBSegment{{
			MessageID:          "<one@test>",
			DecodedStartOffset: 0,
			DecodedEndOffset:   int64(len(raw)),
		}},
	}}
	fileByName := map[string]ImportedNZBFile{"Movie.7z": files[0]}
	readerAt, volumeSizes, totalSize, err := buildImportedArchiveReader(context.Background(), []ImportedArchiveVolume{{Path: "Movie.7z", VolumeIndex: 0}}, fileByName, fetcherStub{data: raw})
	if err != nil {
		t.Fatalf("buildImportedArchiveReader: %v", err)
	}
	reader, err := sevenzip.NewReader(readerAt, totalSize)
	if err != nil {
		t.Fatalf("sevenzip.NewReader: %v", err)
	}
	_, err = inspect7zEntries(reader, volumeSizes)
	if !errors.Is(err, errArchiveCompressionUnsupported) {
		t.Fatalf("expected compression rejection, got %v", err)
	}
}

func TestSplitArchiveRangeAcrossVolumes(t *testing.T) {
	ranges, err := splitArchiveRange(map[int]int64{
		0: 100,
		1: 150,
	}, 80, 120)
	if err != nil {
		t.Fatalf("splitArchiveRange: %v", err)
	}
	if len(ranges) != 2 {
		t.Fatalf("unexpected ranges %+v", ranges)
	}
	if ranges[0].LengthBytes != 20 || ranges[1].EntryOffset != 20 || ranges[1].LengthBytes != 100 {
		t.Fatalf("unexpected cross-volume mapping %+v", ranges)
	}
}

func buildRAR4(solid bool, encrypted bool, method byte, name string, payloadSize uint32) []byte {
	raw := append([]byte{}, []byte("Rar!\x1a\x07\x00")...)
	mainFlags := uint16(0x0100)
	if solid {
		mainFlags |= 0x0008
	}
	if encrypted {
		mainFlags |= 0x0080
	}
	raw = append(raw, rarBlock(0x73, mainFlags, make([]byte, 6))...)
	body := make([]byte, 25+len(name))
	binary.LittleEndian.PutUint32(body[0:4], payloadSize)
	binary.LittleEndian.PutUint32(body[4:8], payloadSize)
	body[18] = method
	binary.LittleEndian.PutUint16(body[19:21], uint16(len(name)))
	copy(body[25:], []byte(name))
	fileFlags := uint16(0)
	if encrypted {
		fileFlags |= 0x0004
	}
	raw = append(raw, rarBlock(0x74, fileFlags, body)...)
	raw = append(raw, make([]byte, int(payloadSize))...)
	raw = append(raw, rarBlock(0x7b, 0, nil)...)
	return raw
}

type rarFixtureEntry struct {
	name        string
	method      byte
	payloadSize uint32
}

func buildRAR4WithEntries(entries []rarFixtureEntry) []byte {
	raw := append([]byte{}, []byte("Rar!\x1a\x07\x00")...)
	raw = append(raw, rarBlock(0x73, 0x0100, make([]byte, 6))...)
	for _, entry := range entries {
		body := make([]byte, 25+len(entry.name))
		binary.LittleEndian.PutUint32(body[0:4], entry.payloadSize)
		binary.LittleEndian.PutUint32(body[4:8], entry.payloadSize)
		body[18] = entry.method
		binary.LittleEndian.PutUint16(body[19:21], uint16(len(entry.name)))
		copy(body[25:], []byte(entry.name))
		raw = append(raw, rarBlock(0x74, 0, body)...)
		raw = append(raw, make([]byte, int(entry.payloadSize))...)
	}
	raw = append(raw, rarBlock(0x7b, 0, nil)...)
	return raw
}

func rarBlock(headType byte, flags uint16, body []byte) []byte {
	raw := make([]byte, 7+len(body))
	raw[2] = headType
	binary.LittleEndian.PutUint16(raw[3:5], flags)
	binary.LittleEndian.PutUint16(raw[5:7], uint16(len(raw)))
	copy(raw[7:], body)
	return raw
}

func loadSevenZipFixture(t *testing.T, name string) []byte {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatalf("go env GOMODCACHE: %v", err)
	}
	root := strings.TrimSpace(string(out))
	raw, err := os.ReadFile(filepath.Join(root, "github.com", "bodgit", "sevenzip@v1.5.1", "testdata", name))
	if err != nil {
		t.Fatalf("read 7z fixture %s: %v", name, err)
	}
	return raw
}
