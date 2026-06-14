# Custom Formats

Custom formats add a positive or negative score bonus to any candidate whose title matches a regular expression. They mirror the concept from Radarr/Sonarr and are stored in the `custom_formats` table.

## How they work

Custom formats are applied in `ranking.ScoreWithPreferences()` after all built-in quality scoring. Each enabled format with a non-empty pattern is tested against the release title. Matching formats have their score added to both the candidate's total score and a separate `custom_format_score` subtotal.

The `custom_format_score` subtotal is stored separately on each release candidate and is used by the upgrade scheduler. When `minimum_upgrade_custom_format_score` is set on a quality profile, an upgrade candidate must improve the custom-format subtotal by at least that amount before it qualifies as an upgrade.

## Format fields

| Field | Type | Description |
|---|---|---|
| `name` | string | Human-readable label shown in the UI and scoring explanations |
| `pattern` | string | Go regular expression tested against the release title (case-sensitive as written; add `(?i)` for case-insensitive) |
| `score` | int | Points to add (positive) or subtract (negative) when the pattern matches |
| `enabled` | bool | When false the format is stored but never evaluated |

## API

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/custom-formats` | List all custom formats |
| `POST` | `/api/custom-formats` | Create a custom format |
| `PUT` | `/api/custom-formats/{id}` | Update a custom format |
| `DELETE` | `/api/custom-formats/{id}` | Delete a custom format |

## Scoring explanations

When a custom format matches, an explanation line is added to the candidate's `explanations` array:

```
Custom format Dolby Vision (+150)
```

If the score is negative:

```
Custom format x265 low-bitrate encode (-100)
```

## Examples

Prefer Dolby Vision releases:

```json
{
  "name": "Dolby Vision",
  "pattern": "(?i)\\b(dovi|dolby.?vision)\\b",
  "score": 150,
  "enabled": true
}
```

Penalise heavily compressed encodes from known groups:

```json
{
  "name": "x265 low-bitrate",
  "pattern": "(?i)\\b(YIFY|YTS|RARBG\\.x265)\\b",
  "score": -300,
  "enabled": true
}
```

Prefer releases with lossless audio:

```json
{
  "name": "Lossless audio",
  "pattern": "(?i)\\b(TrueHD|FLAC|DTS-HD\\.MA)\\b",
  "score": 80,
  "enabled": true
}
```

Boost remux releases (stacks with the built-in +40 remux bonus):

```json
{
  "name": "UHD Remux",
  "pattern": "(?i)\\bUHD\\.Remux\\b",
  "score": 100,
  "enabled": true
}
```

## Relationship to TRaSH Guides

Custom formats can be imported from TRaSH Guides via the Settings > Quality page. Imported formats show `source: trash` in the UI. See [trash-sync.md](trash-sync.md) for how to import them.

Custom formats do not have a separate source field in the database — unlike release block rules, all custom formats can be fully edited and deleted regardless of how they were created.

## Pattern tips

- The pattern is matched against the full release title string (e.g. `Movie.Title.2024.2160p.UHD.BluRay.Remux.DV.TrueHD.Atmos-GROUP`).
- Go regex uses `\b` for word boundaries. In JSON, escape as `\\b`.
- Test your pattern at regex101.com with the Go flavour, then paste it into the `pattern` field.
- Avoid anchoring with `^` or `$` unless you want to match only at the start or end of the full title string.
