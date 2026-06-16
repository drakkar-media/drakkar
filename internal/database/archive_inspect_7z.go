package database

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/hjongedijk/drakkar/internal/stream"
)

var sevenZipCopyCoder = []byte{0x00}

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

func buildImportedArchiveReader(ctx context.Context, volumes []ImportedArchiveVolume, fileByName map[string]ImportedNZBFile, fetcher stream.SegmentFetcher) (io.ReaderAt, map[int]int64, int64, error) {
	spans := make([]stream.SegmentSpan, 0)
	volumeSizes := make(map[int]int64, len(volumes))
	var totalSize int64
	for _, volume := range volumes {
		file, ok := fileByName[volume.Path]
		if !ok {
			return nil, nil, 0, errArchiveHeadersInvalid
		}
		actualSize := importedFileActualSize(ctx, file, fetcher)
		volumeSizes[volume.VolumeIndex] = actualSize
		for _, segment := range file.Segments {
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

func importedFileActualSize(ctx context.Context, file ImportedNZBFile, fetcher stream.SegmentFetcher) int64 {
	if len(file.Segments) == 0 {
		return 0
	}
	aware, ok := fetcher.(interface {
		FetchRangeInfo(ctx context.Context, segment stream.SegmentRange) ([]byte, stream.SegmentSpan, error)
	})
	if !ok {
		return file.FileSizeBytes
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
		return file.FileSizeBytes
	}
	return actual.End
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

type sevenZipInspector struct {
	packPosition uint64
	packSizes    []uint64
	folders      reflect.Value
}

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

func (s *sevenZipInspector) entry(file *sevenzip.File, volumeSizes map[int]int64) (ImportedArchiveEntry, error) {
	fv := reflect.ValueOf(file).Elem()
	folder := int(fv.FieldByName("folder").Int())
	offset := fv.FieldByName("offset").Int()
	coderIDs, encrypted := s.folderCoderInfo(folder)
	method := sevenZipMethodName(coderIDs)
	if len(coderIDs) != 1 || !bytes.Equal(coderIDs[0], sevenZipCopyCoder) {
		if encrypted {
			return ImportedArchiveEntry{}, errArchiveEncrypted
		}
		return ImportedArchiveEntry{}, errArchiveCompressionUnsupported
	}
	archiveOffset := s.folderOffset(folder) + offset
	ranges, err := splitArchiveRange(volumeSizes, archiveOffset, int64(file.UncompressedSize))
	if err != nil {
		return ImportedArchiveEntry{}, errArchiveHeadersInvalid
	}
	entry := ImportedArchiveEntry{
		Path:              file.Name,
		SizeBytes:         int64(file.UncompressedSize),
		PackedSizeBytes:   int64(file.UncompressedSize),
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

func (s *sevenZipInspector) folderOffset(folder int) int64 {
	var offset uint64
	packedOffset := 0
	for i := 0; i < folder; i++ {
		packedStreams := int(s.folders.Index(i).Elem().FieldByName("packedStreams").Uint())
		for j := 0; j < packedStreams; j++ {
			offset += s.packSizes[packedOffset+j]
		}
		packedOffset += packedStreams
	}
	return int64(s.packPosition + offset)
}

func splitArchiveRange(volumeSizes map[int]int64, archiveOffset, size int64) ([]ImportedArchiveRange, error) {
	if size < 0 {
		return nil, fmt.Errorf("invalid archive size")
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
