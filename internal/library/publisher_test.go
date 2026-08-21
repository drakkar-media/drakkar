package library

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/stream"
)

type repoStub struct {
	files         []database.ReleaseVirtualFile
	publicated    []database.CompletedSymlinkEntry
	available     int64
	selected      []int64
	byLibrary     []int64
	pending       []database.PendingRepublishTarget
	matches       []database.SeasonPackEpisodeMatch
	episodeMeta   map[int64]database.EpisodeMetadata
	createCalls   int
	fulfilled     []int64
	virtualData   map[int64][]byte
	virtualErr    map[int64]error
	sourceRelease int64
	upsertErrFor  map[int64]error

	// sourceReleaseErr/episodeMetaErr let a test simulate a real DB failure
	// from FindSourceSelectedReleaseForItem/GetEpisodeMetadataForLibraryItem,
	// distinct from the legitimate "nothing to do" zero-value responses.
	sourceReleaseErr error
	episodeMetaErr   error

	episodeMetaBatchCalls  int
	episodeMetaSingleCalls int

	// republishDelay/republishConcurrency let a test observe whether two
	// concurrent RepublishLibraryItem calls for the same item ever overlap
	// inside the critical section.
	republishDelay          time.Duration
	republishConcurrency    int32 // atomic: current count inside ListSelectedReleasesByLibraryItem
	maxRepublishConcurrency int32 // atomic: highest concurrency observed
}

func (r *repoStub) ListVirtualFilesForRelease(ctx context.Context, selectedReleaseID int64) ([]database.ReleaseVirtualFile, error) {
	return r.files, nil
}

func (r *repoStub) UpsertSymlinkPublication(ctx context.Context, libraryItemID, virtualFileID int64, libraryPath, targetPath string) error {
	if err := r.upsertErrFor[virtualFileID]; err != nil {
		return err
	}
	r.publicated = append(r.publicated, database.CompletedSymlinkEntry{
		PublicationID: virtualFileID,
		Name:          filepath.Base(libraryPath),
		TargetPath:    targetPath,
	})
	return nil
}

func (r *repoStub) MarkReleaseAvailable(ctx context.Context, selectedReleaseID int64) error {
	r.available = selectedReleaseID
	return nil
}

func (r *repoStub) ListSelectedReleasesForPublication(ctx context.Context) ([]int64, error) {
	return r.selected, nil
}

func (r *repoStub) ListSelectedReleasesByLibraryItem(ctx context.Context, libraryItemID int64) ([]int64, error) {
	cur := atomic.AddInt32(&r.republishConcurrency, 1)
	defer atomic.AddInt32(&r.republishConcurrency, -1)
	for {
		old := atomic.LoadInt32(&r.maxRepublishConcurrency)
		if cur <= old || atomic.CompareAndSwapInt32(&r.maxRepublishConcurrency, old, cur) {
			break
		}
	}
	if r.republishDelay > 0 {
		time.Sleep(r.republishDelay)
	}
	return r.byLibrary, nil
}

func (r *repoStub) ListPendingRepublishTargets(ctx context.Context) ([]database.PendingRepublishTarget, error) {
	return r.pending, nil
}

func (r *repoStub) FindSourceSelectedReleaseForItem(_ context.Context, _ int64) (int64, error) {
	if r.sourceReleaseErr != nil {
		return 0, r.sourceReleaseErr
	}
	return r.sourceRelease, nil
}
func (r *repoStub) GetEpisodeMetadataForLibraryItem(_ context.Context, libraryItemID int64) (database.EpisodeMetadata, error) {
	r.episodeMetaSingleCalls++
	if r.episodeMetaErr != nil {
		return database.EpisodeMetadata{}, r.episodeMetaErr
	}
	if r.episodeMeta == nil {
		return database.EpisodeMetadata{}, nil
	}
	return r.episodeMeta[libraryItemID], nil
}
func (r *repoStub) GetEpisodeMetadataForLibraryItems(_ context.Context, libraryItemIDs []int64) (map[int64]database.EpisodeMetadata, error) {
	r.episodeMetaBatchCalls++
	out := make(map[int64]database.EpisodeMetadata, len(libraryItemIDs))
	for _, id := range libraryItemIDs {
		if meta, ok := r.episodeMeta[id]; ok {
			out[id] = meta
		}
	}
	return out, nil
}

func (r *repoStub) FindSeasonPackMatches(_ context.Context, _, _ int64) ([]database.SeasonPackEpisodeMatch, error) {
	return r.matches, nil
}

func (r *repoStub) FulfillEpisodeLibraryItem(_ context.Context, libraryItemID, _, _ int64) error {
	r.fulfilled = append(r.fulfilled, libraryItemID)
	return nil
}
func (r *repoStub) CreateSeasonPackEpisodeItems(_ context.Context, _, _ int64) error {
	r.createCalls++
	return nil
}

func (r *repoStub) OpenVirtualMediaFile(_ context.Context, virtualFileID int64) (stream.VirtualMediaFile, error) {
	if err := r.virtualErr[virtualFileID]; err != nil {
		return nil, err
	}
	return testVF{name: "vf", data: r.virtualData[virtualFileID]}, nil
}

type testVF struct {
	name string
	data []byte
}

func (f testVF) Name() string { return f.name }
func (f testVF) Size() int64  { return int64(len(f.data)) }
func (f testVF) ReadAt(_ context.Context, dst []byte, off int64) (int, error) {
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(dst, f.data[off:])
	if int(off)+n >= len(f.data) {
		return n, io.EOF
	}
	return n, nil
}

func TestPublishSelectedReleaseUnknownMediaType(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID:     11,
				SelectedReleaseID: 77,
				LibraryItemID:     22,
				MediaType:         "manual_nzb",
				Path:              "releases/77/Dune.mkv",
				FileName:          "Dune.mkv",
			},
		},
		virtualData: map[int64][]byte{11: []byte("not-media")},
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	if err := publisher.PublishSelectedRelease(context.Background(), 77); err != nil {
		t.Fatal(err)
	}
	// No host symlink should be created when metadata is insufficient.
	if _, err := os.Stat(filepath.Join(rt.MovieLibraryPath)); err == nil {
		entries, _ := os.ReadDir(rt.MovieLibraryPath)
		if len(entries) > 0 {
			t.Fatalf("expected no host symlink directories, found %v", entries)
		}
	}
	// Release must still be marked available so the FUSE virtual file is accessible.
	if repo.available != 77 {
		t.Fatalf("release not marked available")
	}
}

func TestPublishSelectedReleaseMoviePath(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID:     11,
				SelectedReleaseID: 77,
				LibraryItemID:     22,
				MediaType:         "movie",
				Path:              "releases/77/Dune (2021).mkv",
				FileName:          "Dune (2021).mkv",
				MovieTitle:        "Dune",
				MovieYear:         2021,
				MovieTMDBID:       438631,
			},
		},
		virtualData: map[int64][]byte{11: append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x01}, 32)...)},
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	if err := publisher.PublishSelectedRelease(context.Background(), 77); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(rt.MovieLibraryPath, "Dune (2021) {tmdb-438631}", "Dune (2021).mkv")
	target, err := os.Readlink(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join(rt.FuseMountPath, "content", "releases/77/Dune (2021).mkv") {
		t.Fatalf("unexpected target %s", target)
	}
}

func TestPublishSelectedReleaseEpisodePath(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID:     12,
				SelectedReleaseID: 88,
				LibraryItemID:     23,
				MediaType:         "episode",
				Path:              "releases/88/Loki (2021) - S02E01.mkv",
				FileName:          "Loki (2021) - S02E01.mkv",
				ShowTitle:         "Loki",
				ShowYear:          2021,
				ShowTVDBID:        362472,
				SeasonNumber:      2,
				EpisodeNumber:     1,
			},
		},
		virtualData: map[int64][]byte{12: append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x01}, 32)...)},
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.TVLibraryPath = filepath.Join(root, "tv")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	if err := publisher.PublishSelectedRelease(context.Background(), 88); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(rt.TVLibraryPath, "Loki (2021) {tvdb-362472}", "Season 02", "Loki - S02E01.mkv")
	target, err := os.Readlink(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join(rt.FuseMountPath, "content", "releases/88/Loki (2021) - S02E01.mkv") {
		t.Fatalf("unexpected target %s", target)
	}
}

func TestPublishSelectedReleaseWholeShowPackPublishesEpisodeSymlink(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID:     40,
				SelectedReleaseID: 99,
				LibraryItemID:     548,
				MediaType:         "tv",
				Path:              "releases/99/Yellowstone.S04E01.mkv",
				FileName:          "Yellowstone.S04E01.mkv",
			},
		},
		matches: []database.SeasonPackEpisodeMatch{{
			VirtualFileID:   40,
			VirtualFilePath: "releases/99/Yellowstone.S04E01.mkv",
			FileName:        "Yellowstone.S04E01.mkv",
			LibraryItemID:   25894,
			SeasonNumber:    4,
			EpisodeNumber:   1,
		}},
		episodeMeta: map[int64]database.EpisodeMetadata{
			25894: {
				ShowTitle:     "Yellowstone",
				ShowYear:      2018,
				ShowTVDBID:    341164,
				SeasonNumber:  4,
				EpisodeNumber: 1,
			},
		},
		virtualData: map[int64][]byte{40: append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x01}, 32)...)},
	}
	rt := config.DefaultRuntime()
	rt.TVLibraryPath = filepath.Join(root, "tv")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	var notified []int64
	publisher.SetMediaServerNotifyHook(func(ctx context.Context, libraryItemID int64) error {
		notified = append(notified, libraryItemID)
		return nil
	})

	if err := publisher.PublishSelectedRelease(context.Background(), 99); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(rt.TVLibraryPath, "Yellowstone (2018) {tvdb-341164}", "Season 04", "Yellowstone - S04E01.mkv")
	target, err := os.Readlink(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join(rt.FuseMountPath, "content", "releases/99/Yellowstone.S04E01.mkv") {
		t.Fatalf("unexpected target %s", target)
	}
	if repo.createCalls != 1 {
		t.Fatalf("expected create pass once, got %d", repo.createCalls)
	}
	if len(repo.fulfilled) != 2 || repo.fulfilled[0] != 25894 || repo.fulfilled[1] != 25894 {
		t.Fatalf("expected initial + post-create fulfill passes, got %+v", repo.fulfilled)
	}
	if len(notified) != 2 || notified[0] != 25894 || notified[1] != 25894 {
		t.Fatalf("expected season-pack sibling 25894 notified on both fulfill passes, got %+v", notified)
	}
}

// TestFulfillSeasonPackEpisodesSkipsFulfillWhenSymlinkPublicationFails guards
// against a real bug: fulfillSeasonPackEpisodes used to swallow a failed
// symlink Publish/UpsertSymlinkPublication for a sibling episode (no log, no
// error surfaced) and still unconditionally call FulfillEpisodeLibraryItem
// afterward, marking that episode "available" even though no symlink was
// ever created for it.
func TestFulfillSeasonPackEpisodesSkipsFulfillWhenSymlinkPublicationFails(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID: 40, SelectedReleaseID: 99, LibraryItemID: 548,
				MediaType: "tv", Path: "releases/99/Yellowstone.S04E01.mkv", FileName: "Yellowstone.S04E01.mkv",
			},
			{
				VirtualFileID: 41, SelectedReleaseID: 99, LibraryItemID: 548,
				MediaType: "tv", Path: "releases/99/Yellowstone.S04E02.mkv", FileName: "Yellowstone.S04E02.mkv",
			},
		},
		matches: []database.SeasonPackEpisodeMatch{
			{
				VirtualFileID: 40, VirtualFilePath: "releases/99/Yellowstone.S04E01.mkv", FileName: "Yellowstone.S04E01.mkv",
				LibraryItemID: 25894, SeasonNumber: 4, EpisodeNumber: 1,
			},
			{
				VirtualFileID: 41, VirtualFilePath: "releases/99/Yellowstone.S04E02.mkv", FileName: "Yellowstone.S04E02.mkv",
				LibraryItemID: 25895, SeasonNumber: 4, EpisodeNumber: 2,
			},
		},
		episodeMeta: map[int64]database.EpisodeMetadata{
			25894: {ShowTitle: "Yellowstone", ShowYear: 2018, ShowTVDBID: 341164, SeasonNumber: 4, EpisodeNumber: 1},
			25895: {ShowTitle: "Yellowstone", ShowYear: 2018, ShowTVDBID: 341164, SeasonNumber: 4, EpisodeNumber: 2},
		},
		upsertErrFor: map[int64]error{
			41: errors.New("db unavailable"),
		},
	}
	rt := config.DefaultRuntime()
	rt.TVLibraryPath = filepath.Join(root, "tv")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	var notified []int64
	publisher.SetMediaServerNotifyHook(func(ctx context.Context, libraryItemID int64) error {
		notified = append(notified, libraryItemID)
		return nil
	})

	if err := publisher.PublishSelectedRelease(context.Background(), 99); err != nil {
		t.Fatal(err)
	}

	for _, id := range repo.fulfilled {
		if id == 25895 {
			t.Fatalf("episode 25895 was marked fulfilled despite its symlink publication failing: %+v", repo.fulfilled)
		}
	}
	foundHealthy := false
	for _, id := range repo.fulfilled {
		if id == 25894 {
			foundHealthy = true
		}
	}
	if !foundHealthy {
		t.Fatalf("expected episode 25894 (successful publish) to be fulfilled, got %+v", repo.fulfilled)
	}
	for _, id := range notified {
		if id == 25895 {
			t.Fatalf("episode 25895 was notified to media servers despite its symlink publication failing: %+v", notified)
		}
	}

	if _, err := os.Readlink(filepath.Join(rt.TVLibraryPath, "Yellowstone (2018) {tvdb-341164}", "Season 04", "Yellowstone - S04E01.mkv")); err != nil {
		t.Fatalf("expected symlink for the successfully-published episode, got err: %v", err)
	}
}

// TestFulfillSeasonPackEpisodesBatchesEpisodeMetadataLookup guards a real
// optimization gap: fulfillSeasonPackEpisodes fetched each match's episode
// metadata one row at a time in its loop, reintroducing the exact per-row
// pattern FindSeasonPackMatches (in the same function, just above) was
// already rewritten to batch away. With N season-pack siblings, this must
// be a single batched lookup, not N individual ones.
func TestFulfillSeasonPackEpisodesBatchesEpisodeMetadataLookup(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID: 40, SelectedReleaseID: 99, LibraryItemID: 548,
				MediaType: "tv", Path: "releases/99/Yellowstone.S04E01.mkv", FileName: "Yellowstone.S04E01.mkv",
			},
			{
				VirtualFileID: 41, SelectedReleaseID: 99, LibraryItemID: 548,
				MediaType: "tv", Path: "releases/99/Yellowstone.S04E02.mkv", FileName: "Yellowstone.S04E02.mkv",
			},
			{
				VirtualFileID: 42, SelectedReleaseID: 99, LibraryItemID: 548,
				MediaType: "tv", Path: "releases/99/Yellowstone.S04E03.mkv", FileName: "Yellowstone.S04E03.mkv",
			},
		},
		matches: []database.SeasonPackEpisodeMatch{
			{VirtualFileID: 40, VirtualFilePath: "releases/99/Yellowstone.S04E01.mkv", FileName: "Yellowstone.S04E01.mkv", LibraryItemID: 25894, SeasonNumber: 4, EpisodeNumber: 1},
			{VirtualFileID: 41, VirtualFilePath: "releases/99/Yellowstone.S04E02.mkv", FileName: "Yellowstone.S04E02.mkv", LibraryItemID: 25895, SeasonNumber: 4, EpisodeNumber: 2},
			{VirtualFileID: 42, VirtualFilePath: "releases/99/Yellowstone.S04E03.mkv", FileName: "Yellowstone.S04E03.mkv", LibraryItemID: 25896, SeasonNumber: 4, EpisodeNumber: 3},
		},
		episodeMeta: map[int64]database.EpisodeMetadata{
			25894: {ShowTitle: "Yellowstone", ShowYear: 2018, ShowTVDBID: 341164, SeasonNumber: 4, EpisodeNumber: 1},
			25895: {ShowTitle: "Yellowstone", ShowYear: 2018, ShowTVDBID: 341164, SeasonNumber: 4, EpisodeNumber: 2},
			25896: {ShowTitle: "Yellowstone", ShowYear: 2018, ShowTVDBID: 341164, SeasonNumber: 4, EpisodeNumber: 3},
		},
	}
	rt := config.DefaultRuntime()
	rt.TVLibraryPath = filepath.Join(root, "tv")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	if err := publisher.PublishSelectedRelease(context.Background(), 99); err != nil {
		t.Fatal(err)
	}

	if repo.episodeMetaSingleCalls != 0 {
		t.Fatalf("expected the per-episode metadata lookup to never be called, got %d calls", repo.episodeMetaSingleCalls)
	}
	// isNew=true triggers fulfillSeasonPackEpisodes twice (once directly,
	// once after CreateSeasonPackEpisodeItems) -- one batch call per pass,
	// never one per match.
	if repo.episodeMetaBatchCalls != 2 {
		t.Fatalf("expected exactly 2 batch metadata lookups (one per fulfillSeasonPackEpisodes pass), got %d", repo.episodeMetaBatchCalls)
	}

	for _, path := range []string{
		filepath.Join(rt.TVLibraryPath, "Yellowstone (2018) {tvdb-341164}", "Season 04", "Yellowstone - S04E01.mkv"),
		filepath.Join(rt.TVLibraryPath, "Yellowstone (2018) {tvdb-341164}", "Season 04", "Yellowstone - S04E02.mkv"),
		filepath.Join(rt.TVLibraryPath, "Yellowstone (2018) {tvdb-341164}", "Season 04", "Yellowstone - S04E03.mkv"),
	} {
		if _, err := os.Readlink(path); err != nil {
			t.Fatalf("expected symlink at %s, got err: %v", path, err)
		}
	}
}

func TestRebuildPublications(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{
		selected: []int64{77},
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID:     11,
				SelectedReleaseID: 77,
				LibraryItemID:     22,
				MediaType:         "movie",
				Path:              "releases/77/Dune (2021).mkv",
				FileName:          "Dune (2021).mkv",
				MovieTitle:        "Dune",
				MovieYear:         2021,
				MovieTMDBID:       438631,
			},
		},
		virtualData: map[int64][]byte{11: append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x01}, 32)...)},
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	if err := publisher.RebuildPublications(context.Background()); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(rt.MovieLibraryPath, "Dune (2021) {tmdb-438631}", "Dune (2021).mkv")
	if _, err := os.Readlink(finalPath); err != nil {
		t.Fatal(err)
	}
	if repo.available != 77 {
		t.Fatalf("unexpected available release %d", repo.available)
	}
}

func TestRepublishLibraryItem(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{
		byLibrary: []int64{77},
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID:     11,
				SelectedReleaseID: 77,
				LibraryItemID:     22,
				MediaType:         "movie",
				Path:              "releases/77/Dune (2021).mkv",
				FileName:          "Dune (2021).mkv",
				MovieTitle:        "Dune",
				MovieYear:         2021,
				MovieTMDBID:       438631,
			},
		},
		virtualData: map[int64][]byte{11: append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x01}, 32)...)},
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	if err := publisher.RepublishLibraryItem(context.Background(), 22); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(rt.MovieLibraryPath, "Dune (2021) {tmdb-438631}", "Dune (2021).mkv")
	if _, err := os.Readlink(finalPath); err != nil {
		t.Fatal(err)
	}
}

// TestRepublishLibraryItemSerializesConcurrentCallsForSameItem guards a real
// race: two overlapping RepublishLibraryItem calls for the SAME library
// item (e.g. a manual repair click racing an automatic health-check repair)
// used to run fully concurrently, each independently reading the current
// selection and writing its own symlink/DB target -- risking the on-disk
// symlink and the DB's recorded target ending up pointing at two different
// releases depending on interleaving. Launches two concurrent calls for the
// same item with an artificial delay inside the critical section and
// asserts they never overlap.
func TestRepublishLibraryItemSerializesConcurrentCallsForSameItem(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{
		byLibrary: []int64{77},
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID:     11,
				SelectedReleaseID: 77,
				LibraryItemID:     22,
				MediaType:         "movie",
				Path:              "releases/77/Dune (2021).mkv",
				FileName:          "Dune (2021).mkv",
				MovieTitle:        "Dune",
				MovieYear:         2021,
				MovieTMDBID:       438631,
			},
		},
		virtualData:    map[int64][]byte{11: append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x01}, 32)...)},
		republishDelay: 20 * time.Millisecond,
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = publisher.RepublishLibraryItem(context.Background(), 22)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&repo.maxRepublishConcurrency); got != 1 {
		t.Fatalf("expected the two concurrent calls to never overlap (max concurrency 1), got %d", got)
	}
}

// TestRepublishLibraryItemPropagatesSourceReleaseLookupError guards a real
// gap: when a library item has no selected release of its own (a season-pack
// episode), RepublishLibraryItem falls back to
// FindSourceSelectedReleaseForItem to locate the pack it belongs to. A real
// DB error from that lookup used to be treated identically to "this item
// legitimately has no source release" (sourceID == 0) -- both silently
// returned nil, so RepublishPendingLibrary counted a transient DB failure as
// a successful republish (Republished++) instead of surfacing it as a
// failure to retry.
func TestRepublishLibraryItemPropagatesSourceReleaseLookupError(t *testing.T) {
	root := t.TempDir()
	wantErr := errors.New("db: connection reset")
	repo := &repoStub{
		sourceReleaseErr: wantErr,
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	err := publisher.RepublishLibraryItem(context.Background(), 22)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected the DB error to propagate, got %v (nil means it was silently swallowed as a no-op success)", err)
	}
}

// TestRepublishEpisodeFromSourceReleasePropagatesEpisodeMetadataError mirrors
// the sibling gap one level deeper: once a source release is found,
// republishEpisodeFromSourceRelease looks up the episode's own metadata via
// GetEpisodeMetadataForLibraryItem. A real DB error there used to be treated
// identically to "this item has no usable episode metadata" -- both silently
// returned nil instead of surfacing the transient failure.
func TestRepublishEpisodeFromSourceReleasePropagatesEpisodeMetadataError(t *testing.T) {
	root := t.TempDir()
	wantErr := errors.New("db: connection reset")
	repo := &repoStub{
		sourceRelease:  77,
		episodeMetaErr: wantErr,
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	err := publisher.RepublishLibraryItem(context.Background(), 22)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected the DB error to propagate, got %v (nil means it was silently swallowed as a no-op success)", err)
	}
}

func TestRepublishPendingLibrary(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{
		pending:   []database.PendingRepublishTarget{{LibraryItemID: 22}, {LibraryItemID: 23}},
		byLibrary: []int64{77},
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID:     11,
				SelectedReleaseID: 77,
				LibraryItemID:     22,
				MediaType:         "movie",
				Path:              "releases/77/Dune (2021).mkv",
				FileName:          "Dune (2021).mkv",
				MovieTitle:        "Dune",
				MovieYear:         2021,
				MovieTMDBID:       438631,
			},
		},
		virtualData: map[int64][]byte{11: append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x01}, 32)...)},
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	result, err := publisher.RepublishPendingLibrary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 2 || result.Republished != 2 || result.Failed != 0 {
		t.Fatalf("unexpected result %+v", result)
	}
}

// TestRestartReconstructionIdempotent simulates two publisher lifetimes sharing
// the same repository state — the canonical "after restart" scenario.
// RebuildPublications must produce identical results on every call without
// duplicating, corrupting, or failing on already-published symlinks.
func TestRestartReconstructionIdempotent(t *testing.T) {
	root := t.TempDir()
	file := database.ReleaseVirtualFile{
		VirtualFileID:     11,
		SelectedReleaseID: 77,
		LibraryItemID:     22,
		MediaType:         "movie",
		Path:              "releases/77/Dune (2021).mkv",
		FileName:          "Dune (2021).mkv",
		MovieTitle:        "Dune",
		MovieYear:         2021,
		MovieTMDBID:       438631,
	}
	repo := &repoStub{
		selected:    []int64{77},
		files:       []database.ReleaseVirtualFile{file},
		virtualData: map[int64][]byte{11: append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x01}, 32)...)},
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.FuseMountPath = filepath.Join(root, "vfs")

	finalPath := filepath.Join(rt.MovieLibraryPath, "Dune (2021) {tmdb-438631}", "Dune (2021).mkv")
	want := filepath.Join(rt.FuseMountPath, "content", "releases/77/Dune (2021).mkv")

	// First publisher lifetime: initial publication.
	p1 := NewPublisher(repo, rt, "")
	if err := p1.RebuildPublications(context.Background()); err != nil {
		t.Fatalf("first rebuild failed: %v", err)
	}
	target1, err := os.Readlink(finalPath)
	if err != nil {
		t.Fatalf("symlink missing after first rebuild: %v", err)
	}
	if target1 != want {
		t.Fatalf("unexpected target after first rebuild: %s", target1)
	}

	// Second publisher lifetime: simulates a restart with the same persisted state.
	// Must overwrite the existing symlink atomically without error.
	p2 := NewPublisher(repo, rt, "")
	if err := p2.RebuildPublications(context.Background()); err != nil {
		t.Fatalf("second rebuild (restart) failed: %v", err)
	}
	target2, err := os.Readlink(finalPath)
	if err != nil {
		t.Fatalf("symlink missing after second rebuild: %v", err)
	}
	if target2 != want {
		t.Fatalf("unexpected target after second rebuild: %s", target2)
	}
	if target1 != target2 {
		t.Fatalf("targets differ between rebuilds: %s vs %s", target1, target2)
	}
}

func TestPublishSelectedReleaseFailsWithoutVirtualFiles(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	err := publisher.PublishSelectedRelease(context.Background(), 77)
	if !errors.Is(err, ErrNoVirtualFiles) {
		t.Fatalf("expected ErrNoVirtualFiles, got %v", err)
	}
	if repo.available != 0 {
		t.Fatalf("release should not be marked available, got %d", repo.available)
	}
}

func TestPublishSelectedReleaseRunsPostPublishHook(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID:     11,
				SelectedReleaseID: 77,
				LibraryItemID:     22,
				MediaType:         "movie",
				Path:              "releases/77/Dune (2021).mkv",
				FileName:          "Dune (2021).mkv",
				MovieTitle:        "Dune",
				MovieYear:         2021,
				MovieTMDBID:       438631,
			},
		},
		virtualData: map[int64][]byte{11: append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x01}, 32)...)},
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	var hooked int64
	publisher.SetPostPublishHook(func(ctx context.Context, libraryItemID int64) error {
		hooked = libraryItemID
		return nil
	})

	if err := publisher.PublishSelectedRelease(context.Background(), 77); err != nil {
		t.Fatal(err)
	}
	if hooked != 22 {
		t.Fatalf("unexpected hooked library item %d", hooked)
	}
}

// TestRepublishLibraryItemNotifiesMediaServers guards against the media
// server (Plex/Jellyfin) never learning about an item whose symlink was
// missing or stale and just got repaired -- RepublishLibraryItem previously
// only ever called postPublishHook when isNew was true, which republish
// passes never are, so the item stayed unrecoverably invisible to Plex until
// its own periodic scan.
func TestRepublishLibraryItemNotifiesMediaServers(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{
		byLibrary: []int64{77},
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID:     11,
				SelectedReleaseID: 77,
				LibraryItemID:     22,
				MediaType:         "movie",
				Path:              "releases/77/Dune (2021).mkv",
				FileName:          "Dune (2021).mkv",
				MovieTitle:        "Dune",
				MovieYear:         2021,
				MovieTMDBID:       438631,
			},
		},
		virtualData: map[int64][]byte{11: append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x01}, 32)...)},
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	var postPublishCalled bool
	publisher.SetPostPublishHook(func(ctx context.Context, libraryItemID int64) error {
		postPublishCalled = true
		return nil
	})
	var notified []int64
	publisher.SetMediaServerNotifyHook(func(ctx context.Context, libraryItemID int64) error {
		notified = append(notified, libraryItemID)
		return nil
	})

	if err := publisher.RepublishLibraryItem(context.Background(), 22); err != nil {
		t.Fatal(err)
	}
	if postPublishCalled {
		t.Fatal("postPublishHook (subtitle search) must not run on a republish repair pass")
	}
	if len(notified) != 1 || notified[0] != 22 {
		t.Fatalf("expected mediaServerNotifyHook called once for library item 22, got %v", notified)
	}
}

// TestRebuildPublicationsDoesNotNotifyMediaServers ensures the full startup
// rebuild -- which republishes every pending selected release unconditionally
// -- does not fire a media server refresh per item. Plex/Jellyfin already
// know about all of these from before the restart; notifying on every one
// would hammer the media server on every container restart.
func TestRebuildPublicationsDoesNotNotifyMediaServers(t *testing.T) {
	root := t.TempDir()
	repo := &repoStub{
		selected: []int64{77},
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID:     11,
				SelectedReleaseID: 77,
				LibraryItemID:     22,
				MediaType:         "movie",
				Path:              "releases/77/Dune (2021).mkv",
				FileName:          "Dune (2021).mkv",
				MovieTitle:        "Dune",
				MovieYear:         2021,
				MovieTMDBID:       438631,
			},
		},
		virtualData: map[int64][]byte{11: append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x01}, 32)...)},
	}
	rt := config.DefaultRuntime()
	rt.MovieLibraryPath = filepath.Join(root, "movies")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	var notified []int64
	publisher.SetMediaServerNotifyHook(func(ctx context.Context, libraryItemID int64) error {
		notified = append(notified, libraryItemID)
		return nil
	})

	if err := publisher.RebuildPublications(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(notified) != 0 {
		t.Fatalf("expected no media server notifications during startup rebuild, got %v", notified)
	}
}

// TestRepublishEpisodeFromSourceReleaseRefreshesContentDir guards a gap found
// in the 2026-07-19 audit: republishEpisodeFromSourceRelease (the season-pack
// episode fallback path, reached when a library item has no selected_releases
// of its own and must borrow a virtual file from the pack's source release)
// only refreshed the libraryPath's rclone VFS cache, never the content
// directory the new symlink actually points into -- unlike its two sibling
// publish functions in this file. Uses a real httptest RC server (not an
// empty rcAddr, which every other test in this file uses and which
// short-circuits before any path is ever computed) to prove both refresh
// calls actually reach rclone with the right path.
func TestRepublishEpisodeFromSourceReleaseRefreshesContentDir(t *testing.T) {
	var refreshedDirs []string
	rc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		refreshedDirs = append(refreshedDirs, r.Form.Get("dir"))
		w.WriteHeader(http.StatusOK)
	}))
	defer rc.Close()

	root := t.TempDir()
	repo := &repoStub{
		byLibrary:     nil, // no selected_releases of its own -- forces the source-release fallback
		sourceRelease: 77,
		episodeMeta: map[int64]database.EpisodeMetadata{
			22: {ShowTitle: "Loki", ShowYear: 2021, SeasonNumber: 1, EpisodeNumber: 2},
		},
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID:     11,
				SelectedReleaseID: 77,
				Path:              "releases/77/Loki.S01E02.mkv",
				FileName:          "Loki.S01E02.mkv",
			},
		},
	}
	rt := config.DefaultRuntime()
	rt.TVLibraryPath = filepath.Join(root, "tv")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, rc.URL)

	if err := publisher.RepublishLibraryItem(context.Background(), 22); err != nil {
		t.Fatal(err)
	}

	wantContentDir := filepath.Join(root, "vfs", "content", "releases", "77")
	foundContentDirRefresh := false
	for _, d := range refreshedDirs {
		if d == "/content/releases/77" {
			foundContentDirRefresh = true
		}
	}
	if !foundContentDirRefresh {
		t.Fatalf("expected a refresh for the content directory %s (as /content/releases/77 relative to the mount), got refreshed dirs: %v", wantContentDir, refreshedDirs)
	}
	if len(repo.publicated) != 1 {
		t.Fatalf("expected the symlink to be published, got %+v", repo.publicated)
	}
}

// TestRepublishEpisodeFromSourceReleaseMatchesDoubleEpisodeFile guards the
// bug behind permanently-stuck health-page "Consistency Issues": a combined
// double-episode release file (e.g. "S03E17E18") only parses to its first
// episode number, so a library item for the second episode (E18 here) was
// never matched and its symlink was silently skipped -- Republish Pending
// looked like it did nothing because, for these items, it genuinely did
// nothing. Confirmed live for "NCIS: New Orleans" S03E18 (file S03E17E18).
func TestRepublishEpisodeFromSourceReleaseMatchesDoubleEpisodeFile(t *testing.T) {
	repo := &repoStub{
		byLibrary:     nil,
		sourceRelease: 77,
		episodeMeta: map[int64]database.EpisodeMetadata{
			25716: {ShowTitle: "NCIS: New Orleans", ShowYear: 2014, SeasonNumber: 3, EpisodeNumber: 18},
		},
		files: []database.ReleaseVirtualFile{
			{
				VirtualFileID:     11,
				SelectedReleaseID: 77,
				Path:              "releases/77/NCIS.New.Orleans.S03E17E18.mkv",
				FileName:          "NCIS.New.Orleans.S03E17E18.mkv",
			},
		},
	}
	root := t.TempDir()
	rt := config.DefaultRuntime()
	rt.TVLibraryPath = filepath.Join(root, "tv")
	rt.FuseMountPath = filepath.Join(root, "vfs")
	publisher := NewPublisher(repo, rt, "")

	if err := publisher.RepublishLibraryItem(context.Background(), 25716); err != nil {
		t.Fatal(err)
	}
	if len(repo.publicated) != 1 {
		t.Fatalf("expected the double-episode file's symlink to be published for episode 18, got %+v", repo.publicated)
	}
}
