# Data Flow

Current implemented flow:

1. Process loads `settings.json`.
2. Runtime validates mount separation and writable host paths.
3. Process connects PostgreSQL and Valkey.
4. SQL migrations apply.
5. HTTP API exposes health, status, request, queue, library, release and SSE endpoints.
6. `/api/status` derives integration readiness from config so the frontend can gate Seerr-, Hydra-, and subtitle-provider-backed actions before making doomed upstream calls.
7. Seerr sync paginates through `/api/v1/request` and can import pending movie and TV requests across the full pending request set into `media_requests`, `library_items`, and `queue_items`.
8. When a TMDB API key is configured and Seerr provided a TMDB ID, imported movie and TV requests are enriched in place with canonical titles, years, and IMDb IDs.
9. When TMDB TV details are unavailable but a TVDB API key is configured and Seerr provided a TVDB ID, imported TV requests fall back to TVDB series details for the same title/year/IMDb enrichment pass.
10. Operators can bulk-process pending request-backed library items, which runs explicit NZBHydra2 search across `requested` or retryable `failed` queue rows that do not currently have a selected release.
11. Library search can query NZBHydra2 using ordered metadata-aware fallback queries such as IMDb ID, canonical title/year, alternate episode token formats, and episode-title fallback variants for awkward TV release naming, and now sends structured Newznab parameters like `imdbid`, `tvdbid`, `season`, and `ep` when metadata exists, then ranks returned releases, persists `release_candidates`, and selects the highest-ranked candidate.
12. Release ranking now rejects obvious bad-source results such as CAM/TS, rejects explicit remake-year mismatches for movies, and prefers exact episode tokens over season packs for episodic search results.
13. When repeated search refreshes return the same external release URL, Drakkar carries forward transient candidate `failure_count` and `last_failure_reason` so those results rank lower on later attempts.
14. Candidate sets from fallback Hydra query variants are merged instead of replaced, so later query variants can improve on a previously selected but degraded candidate without losing already seen options.
15. When earlier Hydra query variants only return already rejected candidates, or when the current best candidate still carries transient failure history, library search continues to later query variants instead of stopping on the first non-empty upstream result.
16. When search exhausts all fallback query variants without a selected release, queue failure reasons now distinguish empty result sets from "all candidates rejected" outcomes such as wrong-title, wrong-year, or archive-only incompatibility.
17. When every Hydra query variant errors before any candidate set is established, Drakkar now marks the queue row `failed` with durable search-failure state such as `search_timeout`, `search_auth_error`, `search_rate_limited`, `search_unavailable`, or fallback `search_error` instead of only returning an API error.
18. Before a Hydra query variant is treated as failed, Drakkar now retries one time for transient timeout, rate-limit, or service-unavailable errors, but does not retry authentication failures.
19. When a best candidate has an external NZB URL, Drakkar fetches that small NZB XML document immediately, imports it into the selected release, and advances queue state to `preflight`.
20. If selected-release fetch/import fails, or if post-index archive inspection yields a durable archive reject reason with zero publishable virtual files, Drakkar records the failed candidate, increments `failure_count`, and promotes the next ranked non-rejected candidate automatically.
21. Manual NZB upload stages file under `/mnt/drakkar/staging/nzbs`, validates XML, persists queue item and NZB metadata, then advances queue state to `preflight`.
22. Background discovery now follows a Servarr-like recent-feed model: TV recent Newznab category polling runs every 15 minutes, movie recent polling runs every 60 minutes, and both use category-only Hydra update queries instead of active per-item searches.
23. Hydra search and feed calls now sit behind TTL caches, and successful recent-feed passes persist maintenance cursors so process restarts inside the cache window do not immediately hit Hydra again.
24. Hydra now enforces slower API pacing, deduplicates queued work, and enters an escalating cooldown after upstream `429` responses so one temporarily rate-limited indexer does not keep getting hammered.
25. Playable loose-media files from imported NZBs are indexed into `virtual_files` and `virtual_file_ranges`.
26. Indexed virtual files publish host-side symlinks into metadata-aware movie/tv library paths under `/mnt/drakkar/media/movies/...` and `/mnt/drakkar/media/tv/...`, and persist `symlink_publications`.
27. Automatic post-import segment calibration has been removed from the hot path; publication and playback no longer trigger background NNTP BODY probes just to rescale offsets.
28. `direct_nzb` readers can reconstruct segment ranges and fetch article bodies through NNTP + `yEnc` decode when provider credentials are configured.
29. During playback, the NNTP/yEnc path now parses live `=ypart begin/end` headers and realigns segment spans in memory, so stream reads stop relying on the rough NZB `0.97 * encoded bytes` offset estimate.
30. Decoded article bodies pass through bounded in-memory cache and singleflight dedupe before range slicing.
31. Decoded article bodies also spill to bounded disk cache under `/mnt/drakkar/cache/blocks`.
32. NNTP BODY requests pass through priority scheduler, then reuse pooled authenticated sessions up to provider connection limits.
33. Scheduled NNTP article requests can retry and fall through across enabled Usenet providers when one upstream fails.
34. Open `direct_nzb` FUSE file handles register read-ahead sessions and schedule bounded background fetches after reads, with cancellation on seek or close.
35. Virtual namespace rules expose required FUSE layout model.
36. Queue retry first reuses existing non-rejected candidate rows with external NZB URLs when available, retries from persisted NZB XML when the selected release is manual or otherwise URL-less, and only falls back to a fresh NZBHydra2 search when no viable candidate remains.
37. If the currently selected release already has transient failure history, queue retry first tries to promote a cleaner existing alternative candidate before reusing that degraded selected release again.
38. Automatic next-candidate promotion after manual reject or fetch/import failure now also prefers lower-failure-history fallback candidates before slightly higher-scoring degraded ones.
39. Durable blocklisting now uses both external release URLs and release-signature keys (title/indexer/size/date) so the same bad Usenet release is rejected again even when Hydra returns it under a different link.
40. Operators can also bulk-retry all currently failed queue rows through the same retry logic, instead of retrying one row at a time.
41. Operators can bulk-republish non-available library items that already have selected releases, instead of republishing one library item at a time.
42. After media publication or republish, Drakkar can trigger asynchronous subtitle-provider search using configured preferred languages without blocking queue availability.
43. When that background subtitle search finds candidates and the item still has no stored subtitles, Drakkar auto-downloads the top-ranked provider candidate into published sidecar paths.
44. Library subtitle search can query SubDL with movie/episode metadata, persist `subtitle_candidates`, and let operators download one candidate into published sidecar paths, including zip-only bundles that are unpacked before publication.
45. Default quality profiles now affect release ranking order for resolution, source, codec, language, and size bounds instead of being frontend-only CRUD state.
46. Library and release API endpoints expose durable PostgreSQL-backed queue and selection state for UI consumers.
47. Library catalog groups TV request rows by show for poster-grid views, while detail pages expand back into season/episode availability.
48. Library catalog now reads poster/backdrop/overview from stored movie and TV metadata instead of live per-item TMDB lookups, so large Seerr libraries stay responsive.

Target flow still pending:

Seerr request -> metadata lookup -> NZBHydra2 search -> ranking -> NZB XML fetch -> segment indexing -> virtual file publish -> host symlink publish -> on-demand NNTP range reads.
