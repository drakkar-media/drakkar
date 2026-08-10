# Configuration

Drakkar reads `/app/data/settings.json` on disk (outside git, `0600`) as
its initial config, but most settings are now editable at runtime through
the Settings UI and hot-reload without a restart — the file is no longer
the only way to configure the app.

- `GET /api/settings` / `PUT /api/settings` (admin-only) — read/update
  settings at runtime. Secrets are redacted on read and never logged.
- `GET /api/settings/privacy/status`, `POST /api/settings/privacy/test`
  (admin-only) — privacy-routing (Direct/SOCKS5/WireGuard) status and a
  live reachability test before committing a mode change.
- `POST /api/integrations/probe` — live reachability/auth checks against
  every configured integration, surfaced as "Probe Integrations" in
  Settings.
- `/api/status` reports config-derived readiness for Seerr, NZBHydra2,
  Usenet, metadata, and subtitle providers — frontend actions that need an
  unconfigured integration are disabled rather than making a doomed call.

To bootstrap a fresh install, copy
[data/settings.example.json](/root/nzbproject/data/settings.example.json)
to `data/settings.json` and fill in credentials, or just complete the
first-run setup wizard (`/setup`), which writes the same file.

## Metadata

- `metadata.tmdb.apiKey` enables TMDB enrichment for Seerr-imported movie
  and TV requests (canonical title, release year, IMDb ID).
- `metadata.tvdb.apiKey` enables TVDB fallback enrichment for TV requests
  when TMDB details are unavailable or no TMDB ID was provided.

## Subtitle providers

- `subtitles.providers.subdl.apiKey` enables SubDL search/download.
- `subtitles.providers.opensubtitles` requires `apiKey`, `username`, and
  `password` for authenticated search/download.
- Both providers share a per-provider daily call budget and a per-language
  dedup pass — see [subtitles.md](subtitles.md).

## Privacy routing

- `privacy.mode`: `direct` (default), `socks5`, or `wireguard`. Applies only
  to NZB indexer HTTP traffic and Usenet NNTP traffic — every other
  integration always goes direct. See `internal/privacy`.
