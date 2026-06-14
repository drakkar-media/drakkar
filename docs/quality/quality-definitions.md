# Quality Definitions

Quality definitions set size limits per resolution tier. Unlike Radarr's absolute MB-per-episode limits, Drakkar uses **MB per minute** so the same limit applies correctly to a 90-minute movie and a 45-minute episode.

## How they work

Quality definitions are rows in the `quality_definitions` table. Each row has:

| Column | Type | Description |
|---|---|---|
| `quality_key` | string | Internal identifier (e.g. `1080p`, `2160p`, `720p`) |
| `title` | string | Display name shown in the UI |
| `media_type` | string | `movie`, `tv`, or `both` |
| `min_mb_per_minute` | int | Minimum size. Candidates below this are rejected as too small. 0 = no minimum. |
| `max_mb_per_minute` | int | Maximum size. Candidates above this are rejected as too large. 0 = no maximum. |
| `sort_order` | int | Display order in the UI (no effect on scoring) |

During candidate scoring, the size check is performed in `rejectBySize()`. The expected size range is:

```
min_bytes = min_mb_per_minute * runtime_minutes * 1024 * 1024
max_bytes = max_mb_per_minute * runtime_minutes * 1024 * 1024
```

**If the movie or episode runtime is unknown (0), the size check is skipped entirely.** This prevents incorrectly rejecting releases for items whose metadata has not yet been fetched from TMDB.

## Applying limits

Size limits can be applied at two levels:

### Profile-level (applies to all resolutions)

Set `min_mb_per_minute` and `max_mb_per_minute` on the quality profile. These apply to every candidate regardless of resolution.

### Tier-level (per resolution)

The `TierMBPerMinuteLimits` map on the `Preferences` struct allows per-resolution overrides. In practice this is populated from quality definitions when the workflow service loads preferences.

If both profile-level and tier-level limits are set, **both** are evaluated. A candidate must satisfy whichever check applies to it.

## API

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/quality-definitions` | List all quality definitions |
| `PUT` | `/api/quality-definitions/{id}` | Update min/max MB per minute for a definition |

Only `min_mb_per_minute` and `max_mb_per_minute` are editable. The `quality_key`, `title`, `media_type`, and `sort_order` fields are managed by the system.

## Typical values (TRaSH Guides reference)

These are approximate starting points. Adjust to taste.

| Quality | Min MB/min | Max MB/min | Notes |
|---|---|---|---|
| 720p WEB | 3 | 20 | Standard HD streaming |
| 1080p WEB | 5 | 35 | Full HD streaming |
| 1080p Remux | 20 | 80 | Lossless BR rip |
| 2160p WEB | 10 | 60 | 4K streaming |
| 2160p Remux | 40 | 180 | Lossless 4K BR rip |

Example: a 120-minute movie at 1080p WEB with min=5, max=35 must be between 600 MB and 4200 MB.

## Reject reasons

| Reason | Cause |
|---|---|
| `too_small` | `size_bytes < min_mb_per_minute * runtime_minutes * 1MB` |
| `too_large` | `size_bytes > max_mb_per_minute * runtime_minutes * 1MB` |

Both reasons cause the candidate to be rejected without being blocklisted. Size rejection is a quality mismatch, not a content problem, so the URL remains available for future searches if the profile limits change.

## Common issues

**All candidates rejected as too_small:** The limits may be too aggressive for your indexers, or the runtime stored in the database is wrong. Check the library item's metadata — a zero runtime means size checks are skipped, but an incorrect non-zero runtime causes wrong calculations. Trigger a metadata refresh to correct it.

**No size checks happening at all:** If you see no `too_small` or `too_large` rejections even for obviously wrong-sized files, confirm the movie has a non-zero runtime in its library entry. Size checks are silently skipped when runtime is 0.
