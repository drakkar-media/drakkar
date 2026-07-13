package nzb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/drakkar-media/drakkar/internal/database"
)

type sinkStub struct {
	item     database.QueueSnapshot
	imported database.ImportedNZB
}

func (s *sinkStub) CreateImportedNZB(ctx context.Context, imported database.ImportedNZB) (database.QueueSnapshot, error) {
	s.imported = imported
	s.item = database.QueueSnapshot{
		QueueItemID:     1,
		LibraryItemID:   10,
		LibraryTitle:    imported.FileName,
		IdempotencyKey:  imported.IdempotencyKey,
		NZBFileName:     imported.FileName,
		NZBFileCount:    imported.FileCount,
		NZBSegmentCount: imported.SegmentCount,
		State:           database.QueueIndexing,
	}
	selection := int64(30)
	document := int64(40)
	s.item.SelectedRelease = &selection
	s.item.NZBDocumentID = &document
	return s.item, nil
}

func (s *sinkStub) SetImportedNZBIndexed(ctx context.Context, queueItemID int64) error {
	return nil
}

func TestImporterRejectsInvalidXML(t *testing.T) {
	importer := NewImporter(t.TempDir(), 1024)
	_, err := importer.Import(context.Background(), &sinkStub{}, "bad.nzb", strings.NewReader("<nzb"))
	if err == nil || !strings.Contains(err.Error(), "parse nzb xml") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestImporterRejectsUploadLimit(t *testing.T) {
	importer := NewImporter(t.TempDir(), 4)
	_, err := importer.Import(context.Background(), &sinkStub{}, "big.nzb", strings.NewReader("12345"))
	if !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("expected ErrUploadTooLarge, got %v", err)
	}
}

func TestImporterStagesAndPersists(t *testing.T) {
	importer := NewImporter(t.TempDir(), 1024*1024)
	sink := &sinkStub{}
	item, err := importer.Import(context.Background(), sink, "Dune.nzb", strings.NewReader(sampleNZB))
	if err != nil {
		t.Fatal(err)
	}
	if item.State != database.QueuePreflight {
		t.Fatalf("expected preflight state, got %s", item.State)
	}
	if sink.imported.FileCount != 1 || sink.imported.SegmentCount != 2 {
		t.Fatalf("unexpected import counts %+v", sink.imported)
	}
	if !strings.HasPrefix(sink.imported.IdempotencyKey, "manual-nzb:") {
		t.Fatalf("unexpected key %s", sink.imported.IdempotencyKey)
	}
	if len(sink.imported.Files) != 1 {
		t.Fatalf("expected imported files, got %+v", sink.imported.Files)
	}
	if sink.imported.Files[0].FileName != "Dune (2021).mkv" {
		t.Fatalf("unexpected parsed filename %s", sink.imported.Files[0].FileName)
	}
	if len(sink.imported.Files[0].Segments) != 2 {
		t.Fatalf("unexpected segment metadata %+v", sink.imported.Files[0].Segments)
	}
}

func TestBuildImportedNZBDetectsArchives(t *testing.T) {
	imported, err := BuildImportedNZB("Movie.nzb", []byte(`<?xml version="1.0" encoding="UTF-8"?>
<nzb>
  <file subject="&quot;Movie.part01.rar&quot; yEnc" poster="poster" date="1710000000">
    <groups><group>alt.binaries.test</group></groups>
    <segments><segment bytes="100" number="1">&lt;part1@test&gt;</segment></segments>
  </file>
  <file subject="&quot;Movie.part02.rar&quot; yEnc" poster="poster" date="1710000001">
    <groups><group>alt.binaries.test</group></groups>
    <segments><segment bytes="100" number="1">&lt;part2@test&gt;</segment></segments>
  </file>
</nzb>`), "test-key", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Archives) != 1 {
		t.Fatalf("expected one archive, got %+v", imported.Archives)
	}
	if imported.Archives[0].Kind != "rar" || imported.Archives[0].Status != "pending" {
		t.Fatalf("unexpected archive %+v", imported.Archives[0])
	}
	if len(imported.Archives[0].Volumes) != 2 {
		t.Fatalf("unexpected volumes %+v", imported.Archives[0].Volumes)
	}
	if imported.Archives[0].Volumes[0].Path != "Movie.part01.rar" || imported.Archives[0].Volumes[1].VolumeIndex != 1 {
		t.Fatalf("unexpected archive volumes %+v", imported.Archives[0].Volumes)
	}
}

func TestBuildImportedNZBComputesDecodedRanges(t *testing.T) {
	imported, err := BuildImportedNZB("Dune.nzb", []byte(sampleNZB), "test-key", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Files) != 1 {
		t.Fatalf("expected one file, got %+v", imported.Files)
	}
	file := imported.Files[0]
	if file.FileSizeBytes <= 0 {
		t.Fatalf("expected positive file size, got %d", file.FileSizeBytes)
	}
	if len(file.Segments) != 2 {
		t.Fatalf("expected two segments, got %+v", file.Segments)
	}
	if file.Segments[0].DecodedEndOffset <= file.Segments[0].DecodedStartOffset {
		t.Fatalf("expected decoded range on first segment, got %+v", file.Segments[0])
	}
	if file.Segments[1].DecodedStartOffset != file.Segments[0].DecodedEndOffset {
		t.Fatalf("expected contiguous decoded ranges, got first=%+v second=%+v", file.Segments[0], file.Segments[1])
	}
}
