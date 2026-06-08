# Architecture

Drakkar splits responsibilities into small Go packages:

- `config`: strict `settings.json` parsing, defaults, secret redaction, mount-layout validation
- `database`: PostgreSQL connection and SQL migrations
- `api`: `chi` router, status endpoints, queue endpoints, library/release listing endpoints, republish endpoint, maintenance endpoints, SSE broker
- `api`: mutating endpoints now publish lightweight SSE events so operator pages can refresh immediately after sync/search/retry/republish/subtitle actions
- `api`: `/api/status` now includes config-derived integration readiness so the frontend can disable unconfigured Seerr/Hydra/subtitle actions
- `probe`: live integration reachability/auth checks for configured Seerr, Hydra, Usenet, and subtitle providers, exposed through the settings page
- `workflow`: Seerr request sync, metadata-aware NZBHydra2 fallback query building, candidate scoring and selected-release persistence
- `workflow`: Hydra search fallback now keeps trying later query variants when earlier results are all already rejected
- `workflow`: repeated search refreshes now preserve transient failure history for the same external release URL so previously failing candidates are penalized across attempts
- `workflow`: fallback query passes now merge candidate sets instead of replacing them, so later queries can improve the selected candidate without discarding earlier viable rows
- `workflow`: queue retry now prefers a cleaner alternative existing candidate over reusing a currently selected candidate that already has transient failure history
- `ranking`: release scoring now rejects obvious bad sources and uses movie-year plus episode-token awareness to prefer exact matches over looser packs
- `workflow`: can bulk-process pending request-backed library rows so imported Seerr requests advance without one-by-one search clicks
- `tmdb`: TMDB client used to enrich imported Seerr requests with canonical movie/show titles, years, and IMDb IDs
- `tvdb`: TVDB client used as fallback enrichment for imported TV requests when TMDB details are unavailable
- `fuse`: virtual namespace model, operation matrix, live `/nzbs` virtual file exposure and cancel path, `/content/releases/<release-id>/...` virtual file tree
- `nzb`: NZB XML parsing and segment offset indexing
- `database`: imported NZB persistence now creates `nzb_files`, `nzb_segments`, `virtual_files`, `virtual_file_ranges`
- `library`: publishes indexed virtual files into host-side symlinks and publication metadata, using movie/tv layout when metadata exists, and can rebuild publications on startup
- `library`: can bulk-republish pending selected library items so publication recovery does not require one-by-one item actions
- `library`: exposes release candidates together with failed-attempt history so operators can see fallback progression
- workflow state now distinguishes transient fetch/import failures from durable manual/archive rejection by persisting blocklisted release URLs across future searches
- workflow now also short-circuits archive-incompatible selected releases immediately after indexing when archive inspection found no publishable virtual files
- automatic next-candidate promotion after reject or fetch/import failure now prefers cleaner candidates with lower transient failure history before slightly higher-scoring degraded choices
- operators can explicitly restore a rejected candidate row, which clears its durable blocklist state without requiring a new search
- operators can bulk-restore all rejected candidates for one library item when a whole candidate set was blocked too aggressively
- operators can also skip a currently selected candidate transiently, promoting the next viable fallback without blocklisting the skipped release
- `maintenance`: host library cleanup tasks for orphaned content and stale publication metadata
- `blocklist`: exposes durable blocked release URLs so operators can inspect them, clear one entry, or clear the active set in one action
- `subtitles`: manual subtitle publication, stored-sidecar rebuild, asynchronous post-publish provider search, provider-backed candidate search/download, zip-bundle extraction, and best-candidate auto-download when no subtitles exist yet
- `valkey`: go-redis maintnotifications probing is explicitly disabled for the current Valkey deployment because the server does not implement Redis `CLIENT MAINT_NOTIFICATIONS`
- `subdl`: SubDL HTTP client for subtitle search and raw-file download
- `opensubtitles`: authenticated OpenSubtitles client for search and file-id based download
- `nntp`: article client and segment fetcher
- `nntp`: reusable pooled sessions bounded by provider connection limits
- `nntp`: priority scheduler now sits in front of pooled session execution
- `yenc`: decoded article body handling
- `ranking`: hard rejection and candidate scoring
- `symlink`: deterministic host-side symlink publication
- `stream`: central `VirtualMediaFile` interface, byte-backed reader, range-to-segment mapping, direct NZB reader
- `cache`: keyed request deduplication for concurrent block fetches and bounded byte LRU storage
- `cache`: decoded article bodies now also spill to bounded disk cache

Planned next layers:

- `nntp`, `cache`, `workflow`
- archive offset mapping, subtitle workers
- deeper metadata resolution through TMDB/TVDB and broader Seerr/NZBHydra2 coverage
