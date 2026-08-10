# Subtitles

Subtitle acquisition has a manual upload path plus a provider-backed
search/download path, with per-language deduplication and a per-provider
daily call budget to stay under provider rate limits (~1000 calls/24h per
provider token).

## API

- `GET /api/subtitles` — paginated Subtitle Manager listing across every
  movie/episode (title, missing-languages filter, candidate count).
- `POST /api/subtitles/bulk` — bulk search or delete across selected items.
- `GET /api/subtitles/{libraryItemId}` — persisted subtitle files for one item.
- `GET /api/subtitle-candidates/{libraryItemId}` — persisted provider search results.
- `POST /api/subtitles/{libraryItemId}/search` — run a provider search for one item.
- `POST /api/subtitle-candidates/{id}/download` — download+publish one stored candidate.
- `POST /api/subtitles/{libraryItemId}/upload` — upload a manual `.srt`/`.vtt` file.
- `DELETE /api/subtitle-files/{id}` — delete one persisted subtitle language/provider set.

## Search behavior (`internal/subtitles/service.go`)

- `SearchCandidates` first computes which requested languages the item is
  still missing (`subtractLanguages` against `languagesWithFiles`) and
  returns immediately with zero provider calls if nothing is missing — an
  item that already has every requested language is never re-queried.
- Providers are tried in a deterministic, per-item hash-based rotation
  (`assignedProviderOrder`) rather than always querying every configured
  provider for every item — this spreads load across providers instead of
  hammering all of them on every search.
- Each provider call is gated by a per-provider daily budget
  (`defaultProviderDailyBudget = 900`, tracked via the `app_settings` table
  under key `subtitle_provider_usage`, resetting at UTC day boundary). A
  provider at budget is skipped for the rest of the day rather than erroring.
- The search stops as soon as every requested language has a candidate,
  without necessarily querying every configured provider.
- `SearchAndDownloadBest` downloads the best-ranked candidate per still-missing
  language (not just one candidate overall), falling through to the next
  candidate for that language if the top one fails to download/publish.
- Candidate ranking prefers exact language match, exact episode over
  season-pack results, matching title/year tokens, canonical `sXXeYY`/`2x03`
  release text, and (as a tie-break) OpenSubtitles slightly ahead of SubDL.
- Provider download supports direct raw `.srt`/`.vtt` files and zip-only
  bundles (unpacked in memory, best file picked by language + filename
  similarity to the release title).
- Existing stored subtitle rows for a language block automatic re-download
  for that language — manual/operator-uploaded subtitles are authoritative
  and are never silently overwritten by an automatic search.

## Configured providers

- SubDL
- OpenSubtitles

## Frontend

- `web/src/lib/components/SubtitlePanel.svelte` is the single shared
  implementation (file list, candidate list, search button, SSE-driven
  refresh) used everywhere subtitles are managed — the movie details page's
  "Subs" button, the per-episode Subtitles modal on the TV details page, and
  the standalone Subtitle Manager page (`/subtitles`) all render the same
  component rather than duplicating this logic per page.
- The TV details page shows a per-episode language badge at a glance
  (`episode.subtitleLanguages`, batch-loaded — no per-episode round trip)
  without needing to open the modal.

## Still pending

- Broader packed-subtitle-archive handling beyond simple zip bundles.
