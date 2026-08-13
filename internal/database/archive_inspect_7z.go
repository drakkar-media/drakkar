package database

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"math"
	"reflect"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/drakkar-media/drakkar/internal/stream"
)

// sevenZipCopyCoder is the 7z codec ID for the uncompressed "copy" method --
// the only method archive inspection can serve byte ranges from without
// fully decompressing the entry.
var sevenZipCopyCoder = []byte{0x00}

// inspect7zArchive determines the playable entries of a 7z archive (single-
// or multi-volume) by presenting all of its volumes concatenated as one
// virtual byte stream (buildImportedArchiveReader) to the sevenzip library's
// own header parser, then extracting each entry's real packed byte layout
// via reflection (inspect7zEntries) since that library doesn't expose it.
func inspect7zArchive(ctx context.Context, archive *ImportedArchive, fileByName map[string]ImportedNZBFile, fetcher stream.SegmentFetcher) error {
	readerAt, volumeSizes, totalSize, err := buildImportedArchiveReader(ctx, archive.Volumes, fileByName, fetcher)
	if err != nil {
		return errArchiveHeadersInvalid
	}
	reader, err := sevenzip.NewReader(readerAt, totalSize)
	if err != nil {
		return errArchiveHeadersInvalid
	}
	entries, err := inspect7zEntries(reader, volumeSizes)
	if err != nil {
		return err
	}
	if err := validatePlayableArchiveEntries(entries); err != nil {
		return err
	}
	archive.Entries = entries
	archive.Status = "supported"
	archive.RejectReason = ""
	return nil
}

// buildImportedArchiveReader concatenates every volume's segments into a
// single io.ReaderAt spanning the whole logical archive (as sevenzip.NewReader
// expects for split archives), by offsetting each volume's segment spans by
// the running total of the volumes before it. Also returns each volume's
// measured decoded size (volumeSizes, keyed by VolumeIndex) and the total
// archive size, both needed later to translate a within-archive byte offset
// back into a specific volume + local offset (splitArchiveRange).
func buildImportedArchiveReader(ctx context.Context, volumes []ImportedArchiveVolume, fileByName map[string]ImportedNZBFile, fetcher stream.SegmentFetcher) (io.ReaderAt, map[int]int64, int64, error) {
	spans := make([]stream.SegmentSpan, 0)
	volumeSizes := make(map[int]int64, len(volumes))
	var totalSize int64
	for _, volume := range volumes {
		file, ok := fileByName[volume.Path]
		if !ok {
			return nil, nil, 0, errArchiveHeadersInvalid
		}
		actualSize, _ := importedFileActualSize(ctx, file, fetcher)
		if actualSize < 0 || totalSize > math.MaxInt64-actualSize {
			return nil, nil, 0, errArchiveHeadersInvalid
		}
		volumeSizes[volume.VolumeIndex] = actualSize
		for _, segment := range file.Segments {
			if segment.DecodedStartOffset < 0 || segment.DecodedEndOffset < segment.DecodedStartOffset ||
				totalSize > math.MaxInt64-segment.DecodedEndOffset {
				return nil, nil, 0, errArchiveHeadersInvalid
			}
			spans = append(spans, stream.SegmentSpan{
				MessageID: segment.MessageID,
				Start:     totalSize + segment.DecodedStartOffset,
				End:       totalSize + segment.DecodedEndOffset,
			})
		}
		totalSize += actualSize
	}
	return archiveReaderAt{
		ctx: ctx,
		vf:  stream.NewDirectNzbReader("archive", totalSize, spans, fetcher, nil),
	}, volumeSizes, totalSize, nil
}

// importedFileActualSize does a live fetch of the last segment's real
// yEnc-declared end offset. ok=true means size is a genuine measurement;
// ok=false means the fetch was unavailable or failed and size is only
// file.FileSizeBytes, an unmeasured estimate -- callers that want to prefer
// a measurement over their own rougher estimates need this distinction,
// since file.FileSizeBytes can itself be larger OR smaller than the truth.
func importedFileActualSize(ctx context.Context, file ImportedNZBFile, fetcher stream.SegmentFetcher) (size int64, ok bool) {
	if len(file.Segments) == 0 {
		return 0, false
	}
	aware, ok := fetcher.(interface {
		FetchRangeInfo(ctx context.Context, segment stream.SegmentRange) ([]byte, stream.SegmentSpan, error)
	})
	if !ok {
		return file.FileSizeBytes, false
	}
	last := file.Segments[len(file.Segments)-1]
	_, actual, err := aware.FetchRangeInfo(ctx, stream.SegmentRange{
		MessageID:    last.MessageID,
		RangeStart:   last.DecodedStartOffset,
		RangeEnd:     last.DecodedEndOffset,
		SegmentStart: last.DecodedStartOffset,
		SegmentEnd:   last.DecodedEndOffset,
	})
	if err != nil || actual.End <= 0 {
		return file.FileSizeBytes, false
	}
	return actual.End, true
}

type archiveReaderAt struct {
	ctx context.Context
	vf  *stream.DirectNzbReader
}

func (r archiveReaderAt) ReadAt(p []byte, off int64) (int, error) {
	return r.vf.ReadAt(r.ctx, p, off)
}

func inspect7zEntries(reader *sevenzip.Reader, volumeSizes map[int]int64) ([]ImportedArchiveEntry, error) {
	meta, err := newSevenZipInspector(reader)
	if err != nil {
		return nil, errArchiveHeadersInvalid
	}
	out := make([]ImportedArchiveEntry, 0, len(reader.File))
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		entry, err := meta.entry(file, volumeSizes)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

// sevenZipInspector exposes the packed byte layout (pack stream sizes,
// position, and per-folder coder/stream metadata) that github.com/bodgit/sevenzip
// parses internally but does not export. Archive inspection needs that
// layout to compute each entry's real ArchiveOffset/PackedSizeBytes for byte-
// range streaming, so it is recovered via reflection into the reader's
// unexported "si" (stream info) field. This is inherently coupled to that
// library's internal struct layout and will break if it changes.
type sevenZipInspector struct {
	packPosition uint64
	packSizes    []uint64
	folders      reflect.Value
}

// newSevenZipInspector reaches into reader's unexported stream-info fields
// (packInfo, unpackInfo) via reflection to recover the pack-stream sizes,
// base pack position, and folder table needed to compute each entry's real
// byte layout. Returns errArchiveHeadersInvalid if the expected internal
// fields aren't present, e.g. after an incompatible library upgrade.
func newSevenZipInspector(reader *sevenzip.Reader) (*sevenZipInspector, error) {
	if reader == nil {
		return nil, errArchiveHeadersInvalid
	}
	root := reflect.ValueOf(reader).Elem()
	si := root.FieldByName("si")
	if !si.IsValid() || si.IsNil() {
		return nil, errArchiveHeadersInvalid
	}
	siv := si.Elem()
	packInfo := siv.FieldByName("packInfo")
	unpackInfo := siv.FieldByName("unpackInfo")
	if !packInfo.IsValid() || packInfo.IsNil() || !unpackInfo.IsValid() || unpackInfo.IsNil() {
		return nil, errArchiveHeadersInvalid
	}
	piv := packInfo.Elem()
	uiv := unpackInfo.Elem()
	sizeField := piv.FieldByName("size")
	packSizes := make([]uint64, sizeField.Len())
	for i := 0; i < sizeField.Len(); i++ {
		packSizes[i] = sizeField.Index(i).Uint()
	}
	return &sevenZipInspector{
		packPosition: piv.FieldByName("position").Uint(),
		packSizes:    packSizes,
		folders:      uiv.FieldByName("folder"),
	}, nil
}

// entry builds an ImportedArchiveEntry for file, rejecting anything this
// inspector can't safely serve byte ranges for: only the single-coder,
// uncompressed "copy" method exposes a stable packed byte layout, so any
// other coder (real compression, or an unrecognized coder chain) fails with
// errArchiveCompressionUnsupported, and an encrypted folder fails with
// errArchiveEncrypted. The entry's archive-relative byte range is split
// across volumes via splitArchiveRange since the underlying data may span a
// multi-volume 7z set.
func (s *sevenZipInspector) entry(file *sevenzip.File, volumeSizes map[int]int64) (ImportedArchiveEntry, error) {
	if s == nil || !s.folders.IsValid() || file == nil || file.UncompressedSize > math.MaxInt64 {
		return ImportedArchiveEntry{}, errArchiveHeadersInvalid
	}
	fv := reflect.ValueOf(file).Elem()
	folder := int(fv.FieldByName("folder").Int())
	offset := fv.FieldByName("offset").Int()
	if folder < 0 || folder >= s.folders.Len() || offset < 0 {
		return ImportedArchiveEntry{}, errArchiveHeadersInvalid
	}
	coderIDs, encrypted := s.folderCoderInfo(folder)
	method := sevenZipMethodName(coderIDs)
	if len(coderIDs) != 1 || !bytes.Equal(coderIDs[0], sevenZipCopyCoder) {
		if encrypted {
			return ImportedArchiveEntry{}, errArchiveEncrypted
		}
		return ImportedArchiveEntry{}, errArchiveCompressionUnsupported
	}
	folderOffset, ok := s.folderOffset(folder)
	if !ok || folderOffset > math.MaxInt64-offset {
		return ImportedArchiveEntry{}, errArchiveHeadersInvalid
	}
	archiveOffset := folderOffset + offset
	fileSize := int64(file.UncompressedSize)
	ranges, err := splitArchiveRange(volumeSizes, archiveOffset, fileSize)
	if err != nil {
		return ImportedArchiveEntry{}, errArchiveHeadersInvalid
	}
	entry := ImportedArchiveEntry{
		Path:              file.Name,
		SizeBytes:         fileSize,
		PackedSizeBytes:   fileSize,
		CompressionMethod: method,
		Encrypted:         false,
		Solid:             false,
		VolumeIndex:       0,
		ArchiveOffset:     0,
		Ranges:            ranges,
	}
	if len(ranges) > 0 {
		entry.VolumeIndex = ranges[0].VolumeIndex
		entry.ArchiveOffset = ranges[0].ArchiveOffset
	}
	return entry, nil
}

// folderCoderInfo returns the coder IDs (in application order) used by the
// given folder, along with whether any of them is the AES-256 SHA-256
// encryption coder ({0x06,0xf1,0x07,0x01}).
func (s *sevenZipInspector) folderCoderInfo(folder int) ([][]byte, bool) {
	fv := s.folders.Index(folder).Elem()
	coders := fv.FieldByName("coder")
	out := make([][]byte, 0, coders.Len())
	encrypted := false
	for i := 0; i < coders.Len(); i++ {
		id := coders.Index(i).Elem().FieldByName("id").Bytes()
		copied := make([]byte, len(id))
		copy(copied, id)
		out = append(out, copied)
		if bytes.Equal(copied, []byte{0x06, 0xf1, 0x07, 0x01}) {
			encrypted = true
		}
	}
	return out, encrypted
}

// folderOffset returns the absolute archive byte offset where the given
// folder's packed data begins, computed by summing the pack-stream sizes of
// every preceding folder (offset from packPosition, the base of the pack
// section). The boolean is false when malformed metadata would overflow or
// reference pack streams outside the parsed table.
func (s *sevenZipInspector) folderOffset(folder int) (int64, bool) {
	if s == nil || !s.folders.IsValid() || s.packPosition > math.MaxInt64 || folder < 0 || folder > s.folders.Len() {
		return 0, false
	}
	total := s.packPosition
	packedOffset := 0
	for i := 0; i < folder; i++ {
		rawPackedStreams := s.folders.Index(i).Elem().FieldByName("packedStreams").Uint()
		if rawPackedStreams > math.MaxInt || packedOffset > len(s.packSizes)-int(rawPackedStreams) {
			return 0, false
		}
		packedStreams := int(rawPackedStreams)
		for j := 0; j < packedStreams; j++ {
			size := s.packSizes[packedOffset+j]
			if size > math.MaxInt64-total {
				return 0, false
			}
			total += size
		}
		packedOffset += packedStreams
	}
	return int64(total), true
}

// splitArchiveRange breaks a single [archiveOffset, archiveOffset+size)
// span of the logical concatenated multi-volume archive into per-volume
// ImportedArchiveRange fragments, using volumeSizes to know where each
// volume's bytes start and end within that logical stream. Returns an error
// if the known volumes can't fully account for size (e.g. a missing volume).
func splitArchiveRange(volumeSizes map[int]int64, archiveOffset, size int64) ([]ImportedArchiveRange, error) {
	if archiveOffset < 0 || size < 0 || archiveOffset > math.MaxInt64-size {
		return nil, errArchiveHeadersInvalid
	}
	if size == 0 {
		return nil, nil
	}
	ranges := make([]ImportedArchiveRange, 0, len(volumeSizes))
	entryOffset := int64(0)
	current := int64(0)
	for volumeIndex := 0; ; volumeIndex++ {
		volumeSize, ok := volumeSizes[volumeIndex]
		if !ok {
			break
		}
		if volumeSize < 0 || current > math.MaxInt64-volumeSize {
			return nil, errArchiveHeadersInvalid
		}
		volumeStart := current
		volumeEnd := volumeStart + volumeSize
		current = volumeEnd
		if archiveOffset >= volumeEnd {
			continue
		}
		localStart := archiveOffset - volumeStart
		if localStart < 0 {
			localStart = 0
		}
		available := volumeSize - localStart
		if available <= 0 {
			continue
		}
		length := size - entryOffset
		if length > available {
			length = available
		}
		ranges = append(ranges, ImportedArchiveRange{
			VolumeIndex:   volumeIndex,
			EntryOffset:   entryOffset,
			ArchiveOffset: localStart,
			LengthBytes:   length,
		})
		entryOffset += length
		if entryOffset == size {
			break
		}
	}
	if entryOffset != size {
		return nil, errArchiveHeadersInvalid
	}
	return ranges, nil
}

// sevenZipMethodName renders coderIDs as a human-readable method label:
// "copy" for the single uncompressed coder, or a "+"-joined list of hex
// coder IDs otherwise (used only in rejection paths, since entry() already
// requires the copy method for a successfully returned entry).
func sevenZipMethodName(coderIDs [][]byte) string {
	if len(coderIDs) == 1 && bytes.Equal(coderIDs[0], sevenZipCopyCoder) {
		return "copy"
	}
	parts := make([]string, 0, len(coderIDs))
	for _, id := range coderIDs {
		parts = append(parts, hex.EncodeToString(id))
	}
	return strings.Join(parts, "+")
}
