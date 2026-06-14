# Release Rules

Release rules (stored in the `release_block_rules` table) let you block or penalise releases before they are selected. They are evaluated during scoring in `ranking.ScoreWithPreferences()`, after all other quality checks.

## Rule types

| Type | How it matches |
|---|---|
| `release_group` | Exact match against the parsed release group (the text after the last `-` in the title). Falls back to a substring match on the full title when no group can be parsed. |
| `title_pattern` | Case-insensitive substring match on the full title. Also matches when dots are normalised to spaces, so `AI Upscale` matches `Movie.2024.AI.Upscale-GRP`. |
| `regex` | Full regular expression (`(?i)` prefix is added automatically). |
| `missing_release_group` | Matches any release whose group cannot be parsed — useful for blocking obfuscated or poorly named posts. |

## Actions

- **block** — the candidate is immediately rejected. The reject reason stored in the database is `blocklist:<type>:<pattern>`.
- **penalty** — `score_penalty` points are subtracted from the candidate's score. The candidate is not rejected; it may still be selected if no better candidate exists.

## Scope (media type)

Each rule applies to `movie`, `tv`, or `both`. TV is the effective media type for episode searches (the internal `episode` type is mapped to `tv` before rule evaluation).

## Source field

Rules have a `source` field:

- `custom` — created by you via the UI or API. These can be fully edited and deleted.
- `trash` — imported from TRaSH Guides. The `enabled` and `note` fields can be changed; all other fields are read-only.

## API

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/release-block-rules` | List all rules |
| `POST` | `/api/release-block-rules` | Create a custom rule |
| `PUT` | `/api/release-block-rules/{id}` | Update a rule |
| `DELETE` | `/api/release-block-rules/{id}` | Delete a custom rule (trash rules cannot be deleted) |
| `POST` | `/api/release-block-rules/import` | Bulk import rules (validates all before writing any) |
| `POST` | `/api/release-block-rules/test` | Test a title against all enabled rules |

### Test endpoint

The test endpoint is the fastest way to diagnose why a specific release is being blocked or penalised:

```json
POST /api/release-block-rules/test
{
  "title": "Movie.2024.1080p.WEB-DL.x264-BADGRP",
  "mediaType": "movie"
}
```

Response:

```json
{
  "allowed": false,
  "blocked": true,
  "scorePenalty": 0,
  "matchedRules": [
    {
      "ruleId": 12,
      "type": "release_group",
      "pattern": "BADGRP",
      "action": "block",
      "reason": "release_group matched \"BADGRP\""
    }
  ]
}
```

## Examples

Block all releases from a specific group:

```json
{
  "type": "release_group",
  "pattern": "YIFY",
  "mediaType": "both",
  "action": "block",
  "note": "Low bitrate encodes"
}
```

Apply a score penalty to AI-upscaled releases instead of blocking them:

```json
{
  "type": "title_pattern",
  "pattern": "AI Upscale",
  "mediaType": "both",
  "action": "penalty",
  "scorePenalty": 200,
  "note": "Prefer organic encodes"
}
```

Block any release without a recognisable release group (movies only):

```json
{
  "type": "missing_release_group",
  "pattern": "",
  "mediaType": "movie",
  "action": "block",
  "note": "No scene/P2P group = likely spam"
}
```

## Evaluation order

Rules are evaluated in the order they are returned by the database (`source DESC, rule_type, lower(pattern)`). The first rule with `action: block` that matches causes immediate rejection — subsequent rules are not checked. Penalty rules are all accumulated regardless of order.
