package app

import (
	"context"
	"time"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/mediaprobe"
	"github.com/drakkar-media/drakkar/internal/observability"
)

// embeddedSubtitleProbeBytes bounds how much of a published file's decoded
// bytes the embedded-subtitle-language probe reads. This is a prefix, not
// the whole file: a Matroska container's Tracks element (which carries each
// stream's language tag) is written near the start of the file by every
// muxer Drakkar's releases realistically come from, well within this
// budget, so reading further would only add NNTP load for no extra signal.
const embeddedSubtitleProbeBytes = 32 * 1024 * 1024

// probeEmbeddedSubtitleLanguagesAsync best-effort-detects which subtitle
// languages are already embedded in libraryItemID's current main video
// file, so internal/subtitles can skip downloading a language the release
// already shipped with. Fully async and non-blocking by design (own
// goroutine, own timeout, panic-recovered) -- see the resource-safety
// requirements captured in SESSION_TASKS.md: this must never compete with
// real playback for provider connections, so every read it does goes
// through database.DB.PrefixBytesBackground (background scheduler
// priority, bypasses FUSE) rather than anything resembling a normal
// streaming read.
func probeEmbeddedSubtitleLanguagesAsync(db *database.DB, libraryItemID int64) {
	go func() {
		defer observability.Recover("embedded-subtitle-probe")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		probeEmbeddedSubtitleLanguages(ctx, db, libraryItemID)
	}()
}

func probeEmbeddedSubtitleLanguages(ctx context.Context, db *database.DB, libraryItemID int64) {
	virtualFileID, readerKind, found, err := db.GetMainVirtualFileForLibraryItem(ctx, libraryItemID)
	if err != nil || !found {
		return
	}
	// Only direct_nzb files are supported today -- see
	// database.DB.PrefixBytesBackground's doc comment for why stored_rar
	// (would need real-time decompression to probe safely) and inline
	// (never the main video file) are excluded.
	if readerKind != "direct_nzb" {
		return
	}
	if probed, err := db.EmbeddedSubtitleLanguagesProbed(ctx, virtualFileID); err != nil || probed {
		// Either a real error (skip quietly -- this is best-effort) or
		// already probed for this exact file: nothing to do either way.
		return
	}
	data, ok, err := db.PrefixBytesBackground(ctx, virtualFileID, embeddedSubtitleProbeBytes)
	if err != nil || !ok || len(data) == 0 {
		return
	}
	languages, err := mediaprobe.DetectSubtitleLanguages(ctx, data)
	if err != nil {
		// Couldn't determine (missing ffprobe binary, truncated prefix
		// ffprobe couldn't parse, etc.) -- still record an empty result so
		// this file isn't re-probed (and re-fetched over NNTP) on every
		// future publish/republish event for it.
		languages = nil
	}
	_ = db.SetEmbeddedSubtitleLanguages(ctx, virtualFileID, languages)
}
