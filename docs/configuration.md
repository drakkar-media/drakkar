# Configuration

Drakkar reads `/app/data/settings.json`.

Rules:

- keep file outside git
- use `0600`
- do not expose through API
- do not log secrets

Copy [data/settings.example.json](/root/nzbproject/data/settings.example.json) to `data/settings.json` and fill credentials.

Readiness notes:

- `/api/status` now reports config-derived readiness for Seerr, NZBHydra2, Usenet, metadata, and subtitle providers.
- frontend request/search actions are disabled when the required integration is not configured, so placeholder example settings do not just produce avoidable provider errors.
- `POST /api/integrations/probe` runs live checks against configured integrations and is surfaced in the settings page as `Probe Integrations`.

Metadata notes:

- `metadata.tmdb.apiKey` enables TMDB enrichment for Seerr-imported movie and TV requests
- when enabled, Drakkar updates canonical movie/show titles, release years, and IMDb IDs from TMDB details
- `metadata.tvdb.apiKey` enables TVDB fallback enrichment for Seerr-imported TV requests when TMDB details are unavailable or no TMDB ID was provided
- TVDB fallback currently updates canonical show title, release year, and IMDb ID from TVDB series details

Subtitle provider notes:

- `subtitles.providers.subdl.apiKey` enables SubDL search/download
- `subtitles.providers.opensubtitles` requires `apiKey`, `username`, and `password`
- when enabled, OpenSubtitles uses authenticated search plus download-by-`file_id`
