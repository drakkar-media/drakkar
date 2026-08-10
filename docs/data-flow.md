# Data Flow

## Startup

1. Load and validate `settings.json` (mount separation, writable host paths).
2. Connect PostgreSQL (state) and Valkey (BullMQ-backed search work queue).
3. Apply SQL migrations (`internal/database/migrations` via `cmd/migrate`,
   or automatically on boot).
4. Rebuild publications from DB state (recreate host symlinks for every
   available item) and recover any download interrupted by the previous
   process exit.
5. Start the HTTP API, WebDAV server, FUSE mount, and every scheduled
   background task (search, retry, publish, health checks, maintenance —
   see the Settings > Tasks tab for the full list and intervals).

## Request → available media

1. **Ingestion**: a Seerr request (paginated sync) or a direct library
   search creates/updates a `library_items` row plus a `queue_items` row in
   state `requested`. TMDB (falling back to TVDB if TMDB is unavailable)
   enriches it with canonical title/year/IMDb ID when available.
2. **Search**: `ListPendingLibrarySearchTargets` finds items due for a
   search pass (new pending items past their cooldown, items with a fresh
   selection stuck in `requested`/`selected` needing to resume, and upgrade
   re-checks) and queries NZBHydra2 with metadata-aware fallback query
   variants (IMDb ID, canonical title/year, alternate episode token
   formats). Returned releases are ranked (`internal/ranking`: resolution/
   source/codec/audio/HDR detection, custom formats, release-block rules,
   indexer policy score, quality-profile preferences, compatibility
   warnings) and persisted as `release_candidates`.
3. **Selection**: the highest-ranked non-rejected, non-blocklisted candidate
   is marked selected (`selected_releases`), and the queue item moves to
   `selected` → `fetching_nzb`.
4. **Fetch/import**: the download dispatcher (a priority queue + worker pool
   in `internal/workflow`, NOT the BullMQ search queue) fetches the release's
   NZB, imports it (`nzb_files`/`nzb_segments`/`virtual_files`), and runs an
   early NNTP preflight check. A user can pause a specific queue item at any
   point — this cancels its in-flight fetch, clears its selection, and marks
   it `on_hold` so no automatic scan touches it again until resumed.
5. **Failure handling**: a fetch/import failure, or an archive inspection
   that finds zero publishable virtual files, increments the candidate's
   failure count and promotes the next-best candidate automatically
   (`promoteNextAfterFailure`), rather than leaving the item stuck. A
   release failing 5 times (1-strike per URL/signature, escalating cooldown
   overall) gets durably blocklisted so it isn't reselected on a later
   search.
6. **Publish**: indexed virtual files are published as host-side symlinks
   under metadata-aware movie/TV paths (`internal/library`,
   `symlink_publications`), and the item is marked `available`. For a season
   pack, every other episode's library item covered by the same release is
   also fulfilled here (`fulfillSeasonPackEpisodes` /
   `FindSeasonPackMatches`) — matching now correctly handles a combined
   double-episode filename like `S03E17E18` against BOTH episodes it covers,
   not just the first-parsed one.
7. **Post-publish**: subtitle search runs asynchronously (never blocks
   availability) — see [subtitles.md](subtitles.md) for the per-language
   dedup + provider-rotation design — and the media server (Plex/Jellyfin)
   is notified to refresh.

## Playback

`direct_nzb` and `stored_rar` virtual files are served on demand: a FUSE/
WebDAV read resolves the requested byte range to Usenet segments, fetches
missing article bodies through the NNTP priority scheduler (interactive
reads jump the queue ahead of read-ahead/background fetches) and pooled,
provider-fallback-aware sessions, decodes yEnc in memory (parsing live
`=ypart begin/end` headers rather than trusting the NZB's rough byte-offset
estimate), and serves the range directly — no full-file download, no
temp-file buffering. Decoded bodies pass through a bounded in-memory cache
plus a bounded on-disk spill cache, both keyed with request-level
singleflight dedupe so concurrent reads of the same block don't double-fetch.

## Automatic maintenance (recurring background tasks)

- **Backlog search** (~15 min) and **queue housekeeping** (~10 min: stale-
  state reset, then policy-driven retry/blocklist of failed items) are the
  two main drivers that keep pending/failed items moving without a person
  clicking anything. Both — and the upgrade-search pass — respect an
  item's `on_hold` flag.
- **Health checks** periodically verify published symlinks are actually
  reachable and articles are still fetchable, flagging "Consistency Issues"
  (available with no symlink) for `Republish Pending` (still recoverable)
  or `Reset Orphaned Available` (unrecoverable, re-queue for a fresh search).
- **Storage/content maintenance** prunes orphaned content, stale
  publication metadata, and expired blocklist/auth-token/URL-fetch rows.
