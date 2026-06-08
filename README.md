# Drakkar

Drakkar is low-load virtual Usenet streaming platform in Go. It indexes NZB metadata, publishes virtual media files through native FUSE, and exposes normal host-side symlinks for Plex, Jellyfin and web playback.

Current repo state:

- Milestone 1 foundation implemented: config loader, structured logging, migrations, HTTP API, SSE, task tracking.
- Milestone 2 skeleton implemented: virtual namespace model for `.ids`, `completed-symlinks`, `content`, `nzbs`, plus path separation checks and symlink publisher.
- Early Milestone 4 core pieces implemented: NZB parser, segment range mapper, direct reader interface wiring, cache singleflight dedupe.
- Durable manual NZB ingest implemented: staged upload, strict size limit, XML validation, queue persistence, queue cancellation API.
- Live FUSE `/nzbs` read/unlink path implemented from PostgreSQL-backed queue state.
- Writable FUSE `/nzbs` create/write/flush/release path now stages kernel writes on disk and imports on flush/release.
- DB-backed `/content/releases/<release-id>/...` FUSE infrastructure implemented for persisted virtual files with inline reader support.
- NZB import now persists `nzb_files`, `nzb_segments`, `virtual_files`, and `virtual_file_ranges` for direct loose media candidates.
- Post-import publication now creates host-side symlinks and exposes persisted `/completed-symlinks` entries.
- Publication now uses metadata-aware movie and TV library paths under `/mnt/drakkar/media/movies/...` and `/mnt/drakkar/media/tv/...`; `Imported/...` fallback paths are gone.
- Added first Seerr request sync slice that persists movie and episode requests into PostgreSQL-backed library and queue state.
- Added TMDB-backed enrichment for Seerr-imported movie and TV requests when `metadata.tmdb.apiKey` is configured.
- Added TVDB-backed fallback enrichment for Seerr-imported TV requests when TMDB details are unavailable and `metadata.tvdb.apiKey` is configured.
- Added selected-release NZB fetch/import path that downloads the chosen NZB XML, indexes files and segments, and reuses the existing virtual file publication flow.
- Added automatic candidate fallback during selected-release fetch/import: failed candidates record failure state and the next ranked release is promoted automatically.
- Added earlier archive-incompatibility fallback after import/indexing when archive inspection yields a durable reject reason and no publishable virtual files.
- Added startup publication reconstruction and manual republish endpoint for library items.
- Added `yEnc` decoder and minimal NNTP article client/segment fetcher, wired into `direct_nzb` reads when a Usenet provider is configured.
- Added bounded decoded-article memory cache, singleflight dedupe, and concurrency limiter around NNTP fetches.
- Replaced one-connection-per-request NNTP path with reusable pooled sessions bounded by provider `maxConnections`.
- Added first fetch scheduler layer with interactive/read-ahead/background priorities in front of pooled NNTP execution.
- Added bounded disk cache for decoded article bodies under `/mnt/drakkar/cache/blocks`.
- Added per-open virtual-file read-ahead session manager with seek/close cancellation, wired onto `direct_nzb` FUSE reads.
- Added first multi-provider NNTP fallback/retry layer across enabled Usenet providers.
- `/api/library`, `/api/library/missing`, and `/api/releases/{libraryItemId}` now return real PostgreSQL-backed state instead of placeholder empty lists.
- Added first NZBHydra2 search/ranking slice that stores scored release candidates and selected releases for library items.
- Added metadata-aware NZBHydra2 fallback query building using stored IMDb/title/year and alternate episode token formats.
- Added release-search fallback that continues to later Hydra query variants when earlier results are all already rejected.
- Tightened release ranking with explicit bad-source rejects plus movie-year and episode-token aware scoring so exact matches outrank season packs and remake mismatches.
- Candidate refresh now preserves transient failure history for the same external release URL, so repeated failed results are penalized on later searches instead of starting clean.
- Search now merges candidate sets across fallback Hydra query variants and keeps searching when the current best candidate already carries prior transient failure history.
- Queue retry now promotes a cleaner existing alternative candidate before reusing the currently selected release when that selected candidate already has transient failure history.
- Added live integration probes through `POST /api/integrations/probe` and the settings page so configured Seerr, Hydra, Usenet, and subtitle providers can be reachability/auth-tested before real workflows.
- Added manual release-candidate select/reject workflow with PostgreSQL-backed promotion/fallback and frontend controls on the library page.
- Added bulk rejected-candidate restore for one library item and bulk blocklist clear for operator recovery without row-by-row repair.
- Added first provider-backed subtitle flow: SubDL search, persisted subtitle candidates, operator download, and library-page candidate controls.
- Added non-blocking automatic subtitle-provider search after media publish and republish, with best-candidate auto-download when no subtitles already exist.
- Added zip-only subtitle bundle support in the provider download path, including in-memory extraction of the best matching subtitle file.
- Added first OpenSubtitles provider client with authenticated search and file-id based download.
- `/api/status` now exposes config-derived integration readiness, and the frontend disables Seerr/Hydra-backed actions when those integrations are not configured.
- Added queue retry endpoint and queue-page retry control for failed searchable items.
- Added manual maintenance endpoints for orphaned content, broken media symlinks and orphaned completed-history rows, with cursor tracking in `maintenance_cursors`.
- Added real block-cache prune endpoint and settings-page control for bounded decoded-article disk cache cleanup.
- Background Hydra discovery now follows Servarr-like recent-feed polling: TV every 15 minutes, movies every 60 minutes, using category-only update queries instead of active per-item searches.
- Hydra pressure control now includes queue dedupe, lower active-search concurrency, slower API pacing, and a 30-minute cooldown after `429` rate-limit responses.
- Svelte frontend now uses a multi-page operator layout patterned after the reference app, with dedicated dashboard, library, requests, queue, and settings routes.
- Dashboard and route pages can trigger sync/search/republish plus manual maintenance tasks against the backend API.
- Library catalog now groups TV requests by show instead of one poster per episode, and uses stored DB metadata for poster/overview fields so large Seerr libraries stay fast.

## Layout

```text
cmd/drakkar
internal/api
internal/app
internal/config
internal/database
internal/fuse
internal/nzb
internal/observability
internal/queue
internal/ranking
internal/stream
internal/symlink
migrations
docs
web
```

## Run

1. Copy `data/settings.example.json` to `data/settings.json`.
2. Set file mode to `0600`.
3. Start stack with Docker Compose.

## Status

This repository does not yet satisfy final acceptance criteria from task spec. Foundation ready; streaming path still under construction.
