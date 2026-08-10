# Architecture

Drakkar is a single Go binary (`cmd/drakkar`) that combines an HTTP API, a
WebDAV/FUSE virtual filesystem for on-demand Usenet streaming, and a set of
scheduled background workers, backed by PostgreSQL. The SvelteKit frontend
(`web/`) is built and embedded into the binary (`internal/frontend`).

## Package map (`internal/`)

- `api` — chi router; every HTTP endpoint (status, auth, queue, library,
  subtitles, health, settings, downloads, SABnzbd-compatible API, webhooks)
  and the SSE broker mutating endpoints publish to so pages refresh live.
- `app` — process wiring: reads config, opens the DB, constructs every
  service, registers scheduled tasks (search, retry, publish, maintenance,
  health checks), starts the HTTP/WebDAV/FUSE servers.
- `auth` — session + API token authentication, `auth_tokens` table (sessions
  and API tokens share one table, discriminated by a `kind` column),
  first-run setup gating, multi-user/role management.
- `workflow` — the core release lifecycle: search (NZBHydra2), candidate
  ranking/selection, fetch/import, fallback-to-next-candidate on failure,
  queue state machine (`queue_items`), the download dispatcher (priority
  queue + worker pool that actually fetches/imports releases), per-item
  pause/resume (cancels the in-flight fetch, marks the item `on_hold`,
  excluded from all automatic search/retry/upgrade scans until resumed).
- `catalog` — read-side library/dashboard views: `LibraryDetail`,
  per-season/episode breakdown (TMDB-backed where available, falling back to
  local DB rows), `CurrentFile` (the actual file backing a selected release,
  resolved via `symlink_publications` since a season pack's virtual files
  all live under the pack's own `selected_release_id`, not each sibling
  episode's).
- `ranking` — release scoring: resolution/source/codec/audio/HDR detection,
  custom formats, release-block rules, indexer policy scores, playback
  compatibility warnings (DV/TrueHD-Atmos/AV1), quality-profile preferences.
- `blocklist` — durable blocked-release-URL store (operator-visible,
  clearable per-entry or in bulk).
- `subtitles` — provider search/download orchestration; see
  [subtitles.md](subtitles.md) for the current per-language-dedup +
  provider-rotation + daily-budget design. `subdl` and `opensubtitles` are
  the two provider HTTP clients.
- `library` — publishes indexed virtual files to host-side symlinks
  (`symlink_publications`), season-pack episode fan-out
  (`FindSeasonPackMatches`/`CreateSeasonPackEpisodeItems`, matching a
  double-episode filename like `S03E17E18` against both episodes it
  covers), startup publication rebuild.
- `maintenance` — scheduled cleanup: orphaned content, stale publication
  metadata, NZB article health checks, storage/content maintenance passes.
- `privacy` / `privacy/wireguard` — optional privacy-routing layer (Direct /
  SOCKS5 / userspace WireGuard) applied only to indexer HTTP and Usenet NNTP
  traffic; every other integration (Plex, Jellyfin, Seerr, TMDB, TVDB,
  subtitle providers) always goes direct. Runtime-hot-reloadable without a
  restart.
- `nntp` — article client, connection pooling bounded by provider
  `maxConnections`, priority scheduler (interactive/read-ahead/background),
  provider fallback/retry.
- `nzb` — NZB XML parsing, segment offset indexing.
- `archive` — multi-volume RAR detection/inspection for `stored_rar` virtual
  files (releases posted as a compressed archive rather than a loose media
  file).
- `stream` — `VirtualMediaFile` interface, byte-backed reader, range-to-
  segment mapping; `direct_nzb` and `stored_rar` are the two reader kinds.
- `cache` — keyed request deduplication for concurrent block fetches, bounded
  in-memory LRU plus a bounded on-disk spill cache for decoded article bodies.
- `dav` — WebDAV server exposing the virtual filesystem to Plex/Jellyfin via
  rclone.
- `database` — PostgreSQL access + `migrations/` (plain numbered `.sql`
  files, tracked in `schema_migrations`, applied via `cmd/migrate` or on
  startup).
- `config` — `settings.json` parsing/defaults/secret-redaction; most settings
  are hot-reloadable at runtime, not just read at startup.
- `probe` — live reachability/auth checks for configured Seerr, Hydra,
  Usenet, and subtitle providers, exposed through the settings page.
- `tmdb` / `tvdb` — metadata enrichment for Seerr-imported requests, with
  TVDB as fallback when TMDB is disabled/unavailable.
- `seerr`, `hydra`, `plex`, `jellyfin`, `mediaserver` — external integration
  clients.
- `policy` — user-configurable queue-management policy (auto blocklist+search
  vs. leave-alone decisions for failed items).
- `observability` — structured (zerolog/JSON) logging to stdout + a log file
  consumed by the Settings > Logs UI (`/api/logs` bounds every read to a
  fixed recent window rather than the whole file — the file itself is not
  yet rotated).
- `queue` — SABnzbd-compatible queue/history API shim.

## Data flow at a glance

See [data-flow.md](data-flow.md) for the request→search→select→fetch→publish
pipeline in detail.
