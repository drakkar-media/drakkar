# A release was blocked (runtime blocklist)

The runtime blocklist (`blocklist_items` table) prevents releases that have already failed from being selected again. This document explains how to diagnose and clear blocklist entries.

## What creates a blocklist entry

A blocklist entry is created whenever a workflow failure triggers the `blocklist_and_search` policy action. The following failure reasons create blocklist entries by default:

| Failure reason | Policy action |
|---|---|
| `missing_articles` | blocklist_and_search |
| `archive_rejected` / `encrypted_archive` | blocklist_and_search |
| `nzb_parse_failed` | blocklist_and_search |
| `nzb_fetch_failed` | blocklist_and_search |
| `nzb_fetch_4xx` (401, 404, 410, 451) | blocklist_and_search |
| `bad_source` | blocklist_and_search |

Blocklist entries are also created manually from the UI when you click "Block and search again" on a queue item.

`preflight_failed` triggers `search_again` (not blocklist), so a preflight failure retries from the next candidate without blocklisting the URL.

## Blocklist key formats

Each blocklist entry has a `key` field with one of two formats:

**External URL key** — matches a specific NZB download URL:
```
external_url:https://indexer.example.com/get/123456/nzb
```

**Release signature key** — fuzzy fingerprint when a URL is not available or as a second key alongside the URL:
```
release_signature:<normalised-title>|<normalised-indexer>|<sizeMB>|<YYYY-MM-DD>
```

Example:
```
release_signature:movie title 2024 1080p web dl x264 group|nzbgeek|4821|2024-11-03
```

Title normalisation: lower-case, punctuation (`.`, `-`, `_`, brackets) replaced with spaces, multiple spaces collapsed.

When a new search result arrives, it is checked against both key formats. If either matches, the candidate is marked as rejected before scoring even begins.

## TTL (expiry)

By default, blocklist entries expire after 30 days (`BlocklistTTLDays` in the policy settings). Expired entries are ignored during candidate filtering but remain in the table until cleaned up. The blocklist stats endpoint shows active vs expired counts.

To change the TTL: Settings > Queue Policy > Blocklist TTL days.

## Viewing the blocklist

In the UI: Settings > Blocklist.

The table shows each entry's key type, reason, creation date, and expiry. You can filter by reason and search by key text.

Via API:
```
GET /api/blocklist?page=1&pageSize=50&reason=missing_articles
```

Response:
```json
{
  "items": [...],
  "page": 1,
  "pageSize": 50,
  "total": 42,
  "totalPages": 1
}
```

Each item includes `releaseTitle`, `indexerName`, `sizeBytes`, and `postedAt` when the system can resolve them from the database.

## Clearing blocklist entries

**Clear a single entry** — UI: click the delete icon next to the entry. API:
```
DELETE /api/blocklist/{id}
```

**Clear all entries** — UI: Settings > Blocklist > Clear All. API:
```
DELETE /api/blocklist
```

**Clear by reason** — clears only entries with a specific failure reason. API:
```
DELETE /api/blocklist?reason=missing_articles
```

After clearing a blocklist entry, trigger a new search for the affected item. The previously-blocked release will be reconsidered.

## Release is still being rejected after clearing the blocklist

If a release is still rejected after you cleared its blocklist entry, the rejection may be coming from a different layer:

1. **Release block rule** — check Settings > Quality > Release Rules. Use the test endpoint to confirm:
   ```
   POST /api/release-block-rules/test
   {"title": "...", "mediaType": "movie"}
   ```

2. **Hard rejection** — CAM/TS detection, raw BR disc, hardcoded subtitles, or size limits. These are evaluated regardless of blocklist state. Check the `explanations` array on the rejected candidate.

3. **Wrong title** — the release title does not contain the movie/show title as expected. This can happen with obfuscated NZB subjects or alternate release titles not in TMDB.

## Diagnosing why a specific candidate was blocked

In the UI, open the library item's release candidates list. Rejected candidates show their reject reason. Expand the explanations to see the full scoring log.

For releases rejected by the blocklist, the reason is the original failure reason (e.g. `missing_articles`), not the text `blocklisted`.
