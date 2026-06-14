# Indexer rate limits

Drakkar routes all indexer traffic through NZBHydra2, which handles per-indexer rate limiting internally. This document covers how Drakkar responds to rate limit conditions from both NZBHydra2 and individual NZB indexers.

## Two separate rate limit scenarios

### 1. NZBHydra2 search rate limits

When a search request to NZBHydra2 returns an HTTP 429 or a response body containing "rate limit", Drakkar:

1. Classifies the failure as `search_rate_limited`.
2. Applies an exponential backoff before the next search attempt.

Backoff schedule (each hit escalates to the next tier):

| Hit count | Cooldown |
|---|---|
| 1 | 15 minutes |
| 2 | 30 minutes |
| 3 | 60 minutes |
| 4 | 3 hours |
| 5 | 6 hours |
| 6 | 12 hours |
| 7+ | 24 hours |

Each successful search decrements the hit counter by 1. The cooldown resets to zero if the counter reaches 0 after successful searches.

During cooldown, Drakkar does not attempt new searches for any item. Items already in `selected` state continue to progress through NZB fetch and preflight.

### 2. NZB download (fetch) rate limits

When fetching the NZB file for a selected release, HTTP 403 responses from the indexer (commonly NZBFinder quota exhaustion) are treated differently from other errors:

| HTTP status | Classification | Action |
|---|---|---|
| 403 | `nzb_fetch_403` | `retry_later` — do not blocklist, try again later |
| 401, 404, 410, 451 | `nzb_fetch_4xx` | `blocklist_and_search` — permanent error, pick a different release |
| Other failures | `nzb_fetch_failed` | `blocklist_and_search` — blocklist this URL, try next candidate |

The `retry_later` action leaves the queue item in the `failed` state. The next scheduled maintenance cycle will attempt the download again. This means a 403 (quota exhausted) will be automatically retried when the quota resets, without losing the selected release or triggering a new search.

## Diagnosing rate limit problems

### NZBHydra2 search is being rate limited

In the application logs, look for entries like:
```
level=warn msg="search failed" reason="search_rate_limited" cooldownUntil="2024-11-03T15:30:00Z"
```

In NZBHydra2's own UI, check the "Stats" page for per-indexer hit counts and any indexer-specific rate limit messages.

**Resolution:** NZBHydra2's rate limit settings for each indexer are configured within NZBHydra2 itself. Reduce the search frequency there, or increase the configured API limits with the indexer.

### NZB fetch returns 403 repeatedly

If queue items stay in `failed` with reason `nzb_fetch_403`:

1. Check your NZBFinder (or other indexer) account quota. The quota resets monthly.
2. If you have multiple NZB indexers, Drakkar tries the URL from whichever indexer returned that result. A 403 does not trigger a new search — the selected release stays selected.
3. Once quota resets, the next maintenance cycle retries automatically (within 30 minutes by default).

To force an immediate retry without waiting for the next cycle: in the UI, click the queue item's Retry button.

## Configuring NZBHydra2 per-indexer limits

Drakkar exposes an `indexer` configuration block that is passed to the workflow service:

```yaml
indexer:
  minimum_age_minutes: 0    # minimum post age before grabbing (0 = no limit)
  retention_days: 1000      # reject candidates older than this many days
  maximum_size_mb: 0        # hard cap on size regardless of quality limits (0 = no cap)
```

These are applied in `buildSearchCandidates()` as pre-filter checks before scoring:

- `retention_days`: candidates older than `now - retention_days` are marked rejected with reason `too_old`.
- `minimum_age_minutes`: candidates newer than this are rejected with `too_new` (separate from the profile-level `minimum_age_hours`).
- `maximum_size_mb`: candidates larger than this are rejected with `too_large`.

## Search caching

Drakkar caches NZBHydra2 search results to avoid hammering the API:

- **Search cache:** per-query results are cached for `search_cache_ttl_seconds` (default 1 hour). Identical searches within the TTL are served from cache.
- **Feed cache:** recent-RSS results are cached for `feed_cache_ttl_seconds` (default 1 hour).

If you want fresh results immediately (e.g. after configuring a new indexer), the cache expires naturally. There is no manual cache-clear endpoint — restart the application to clear in-memory caches.

## NZBHydra2 is returning no results

No results is not a rate limit — it is classified as `no_release_found` and the policy action is `do_nothing` (wait for next cycle). Common causes:

- The item's title or year does not match what the indexer has indexed.
- The indexers configured in NZBHydra2 do not carry the content (e.g. only TV-focused indexers for a movie search).
- The retention period is exceeded — the NZB is too old to be on any server.

Check NZBHydra2's search log directly to see what queries were sent and what responses were received.
