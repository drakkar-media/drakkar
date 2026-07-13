package fuse

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/stream"
)

type providerStub struct {
	items         []database.NZBMountEntry
	content       []database.ContentMountEntry
	completed     []database.CompletedSymlinkEntry
	canceled      int64
	importedName  string
	importedBytes []byte
	virtualData   map[int64][]byte
}

func (p *providerStub) ListNZBMountEntries(ctx context.Context) ([]database.NZBMountEntry, error) {
	return p.items, nil
}

func (p *providerStub) CancelNZBDocument(ctx context.Context, nzbDocumentID int64) error {
	p.canceled = nzbDocumentID
	return nil
}

func (p *providerStub) ListContentMountEntries(ctx context.Context) ([]database.ContentMountEntry, error) {
	return p.content, nil
}

func (p *providerStub) ListCompletedSymlinkEntries(ctx context.Context) ([]database.CompletedSymlinkEntry, error) {
	return p.completed, nil
}

func (p *providerStub) OpenVirtualMediaFile(ctx context.Context, virtualFileID int64) (stream.VirtualMediaFile, error) {
	data, ok := p.virtualData[virtualFileID]
	if !ok {
		return nil, errors.New("missing")
	}
	return stream.NewByteVirtualFile("inline.bin", data), nil
}

func (p *providerStub) ImportNZBPath(ctx context.Context, fileName, path string) (database.QueueSnapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return database.QueueSnapshot{}, err
	}
	p.importedName = fileName
	p.importedBytes = raw
	return database.QueueSnapshot{}, nil
}

func TestMakeVisibleNZBEntriesDeduplicates(t *testing.T) {
	items := []database.NZBMountEntry{
		{DocumentID: 1, FileName: "Dune.nzb"},
		{DocumentID: 2, FileName: "Dune.nzb"},
		{DocumentID: 3, FileName: "Loki"},
	}
	got := makeVisibleNZBEntries(items)
	if got[0].VisibleName != "Dune.nzb" {
		t.Fatalf("unexpected first name %s", got[0].VisibleName)
	}
	if got[1].VisibleName != "Dune.2.nzb" {
		t.Fatalf("unexpected duplicate name %s", got[1].VisibleName)
	}
	if got[2].VisibleName != "Loki.nzb" {
		t.Fatalf("unexpected extension handling %s", got[2].VisibleName)
	}
}

func TestNZBsDirUnlinkCancelsDocument(t *testing.T) {
	provider := &providerStub{
		items: []database.NZBMountEntry{
			{DocumentID: 42, FileName: "Dune.nzb"},
		},
	}
	node := &NZBsDirNode{provider: provider}
	if errno := node.Unlink(context.Background(), "Dune.nzb"); errno != 0 {
		t.Fatalf("unexpected errno %d", errno)
	}
	if provider.canceled != 42 {
		t.Fatalf("expected cancel 42, got %d", provider.canceled)
	}
}

func TestUploadHandleCommitImportsPath(t *testing.T) {
	provider := &providerStub{}
	dir := &NZBsDirNode{
		provider:       provider,
		stagingDir:     t.TempDir(),
		maxUploadBytes: 1024,
	}
	file, err := os.CreateTemp(dir.stagingDir, "*.nzb")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	defer file.Close()

	node := &UploadNode{name: "Dune.nzb", path: file.Name(), dir: dir}
	handle := &uploadHandle{file: file, node: node}
	payload := []byte(`<?xml version="1.0"?><nzb></nzb>`)
	if _, errno := handle.Write(context.Background(), payload, 0); errno != 0 {
		t.Fatalf("unexpected write errno %d", errno)
	}
	if err := handle.commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.importedName != "Dune.nzb" {
		t.Fatalf("unexpected imported name %s", provider.importedName)
	}
	if string(provider.importedBytes) != string(payload) {
		t.Fatalf("unexpected imported bytes %q", string(provider.importedBytes))
	}
}

func TestUploadHandleWriteLimit(t *testing.T) {
	provider := &providerStub{}
	dir := &NZBsDirNode{
		provider:       provider,
		stagingDir:     t.TempDir(),
		maxUploadBytes: 4,
	}
	file, err := os.CreateTemp(dir.stagingDir, "*.nzb")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	defer file.Close()

	handle := &uploadHandle{file: file, node: &UploadNode{name: "big.nzb", path: file.Name(), dir: dir}}
	if _, errno := handle.Write(context.Background(), []byte("12345"), 0); errno != syscall.ENOSPC {
		t.Fatalf("expected ENOSPC, got %d", errno)
	}
}

func TestReleaseDirAndVirtualFileRead(t *testing.T) {
	provider := &providerStub{
		content: []database.ContentMountEntry{
			{
				VirtualFileID:     11,
				SelectedReleaseID: 77,
				Path:              "releases/77/Dune.mkv",
				FileName:          "Dune.mkv",
				SizeBytes:         11,
				ReaderKind:        "inline",
			},
		},
		virtualData: map[int64][]byte{
			11: []byte("hello world"),
		},
	}
	releases := &ReleasesDirNode{provider: provider}
	entries, errno := releases.releaseDirEntries(context.Background())
	if errno != 0 || len(entries) != 1 || entries[0].Name != "77" {
		t.Fatalf("unexpected entries %+v errno=%d", entries, errno)
	}
	releaseDir := &ReleaseDirNode{provider: provider, releaseID: 77}
	items, errno := releaseDir.releaseItems(context.Background())
	if errno != 0 || len(items) != 1 || items[0].FileName != "Dune.mkv" {
		t.Fatalf("unexpected release items %+v errno=%d", items, errno)
	}
	fileNode := &VirtualFileNode{provider: provider, virtualFileID: 11, fileName: "Dune.mkv", sizeBytes: 11}
	result, errno := fileNode.Read(context.Background(), nil, make([]byte, 5), 6)
	if errno != 0 {
		t.Fatalf("unexpected errno %d", errno)
	}
	data, status := result.Bytes(nil)
	if status != 0 || string(data) != "world" {
		t.Fatalf("unexpected read %q status=%d", string(data), status)
	}
}

func TestCompletedSymlinksLookup(t *testing.T) {
	provider := &providerStub{
		completed: []database.CompletedSymlinkEntry{
			{PublicationID: 1, Name: "Dune.mkv", TargetPath: "/content/releases/77/Dune.mkv"},
		},
	}
	items, err := provider.ListCompletedSymlinkEntries(context.Background())
	if err != nil || len(items) != 1 || items[0].Name != "Dune.mkv" {
		t.Fatalf("unexpected completed entries %+v err=%v", items, err)
	}
}
