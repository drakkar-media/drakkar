package queue

import (
	"context"
	"strings"
	"testing"

	"github.com/hjongedijk/drakkar/internal/database"
	"github.com/hjongedijk/drakkar/internal/nzb"
)

const sampleNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb>
  <file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000">
    <groups><group>alt.binaries.movies</group></groups>
    <segments>
      <segment bytes="1000" number="1">&lt;msg1&gt;</segment>
    </segments>
  </file>
</nzb>`

func TestServiceImportAndCancel(t *testing.T) {
	repo := NewMemoryRepository()
	service := NewService(repo, nzbImporter(t))

	item, err := service.ImportNZB(context.Background(), "Dune.nzb", strings.NewReader(sampleNZB))
	if err != nil {
		t.Fatal(err)
	}
	if item.State != database.QueuePreflight {
		t.Fatalf("expected preflight state, got %s", item.State)
	}
	if err := service.CancelNZB(context.Background(), *item.NZBDocumentID); err != nil {
		t.Fatal(err)
	}
	items, err := service.ListQueue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if items[0].State != database.QueueFailed || items[0].FailureReason != "cancelled" {
		t.Fatalf("unexpected cancel result %+v", items[0])
	}
}

func TestDetectUploadName(t *testing.T) {
	if got := DetectUploadName("test.nzb", ""); got != "test.nzb" {
		t.Fatalf("got %s", got)
	}
	if got := DetectUploadName("", `attachment; filename="other.nzb"`); got != "other.nzb" {
		t.Fatalf("got %s", got)
	}
}

func nzbImporter(t *testing.T) *nzb.Importer {
	t.Helper()
	return nzb.NewImporter(t.TempDir(), 1024*1024)
}
