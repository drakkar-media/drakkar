# Release Filtering

This document describes every layer of filtering applied to a Usenet release candidate before it is selected. The layers are applied in order; a rejection at any layer prevents later layers from running.

## Filtering pipeline

```
1. Runtime blocklist check     (in ReplaceSearchCandidates, before scoring)
2. Title match check           (ScoreWithPreferences)
3. Minimum age check           (ScoreWithPreferences)
4. Profile exclude patterns    (ScoreWithPreferences)
5. Hard rejections             (ScoreWithPreferences)
6. Custom format scoring       (ScoreWithPreferences)
7. Release block rules         (ScoreWithPreferences)
```

---

### 1. Runtime blocklist

Before any candidate is scored, `ReplaceSearchCandidates` loads all active `blocklist_items` rows into an in-memory map and checks each candidate against it. Candidates that match are marked `rejected=true` with the reason stored in the database. They are still inserted as rejected release candidates so you can see them in the UI.

Two key formats are checked per candidate:

- `external_url:<url>` — exact match on the NZB download URL.
- `release_signature:<title>|<indexer>|<sizeMB>|<date>` — fuzzy fingerprint. Title is normalised (lower-case, punctuation replaced with spaces), size is rounded to the nearest MB, date is truncated to UTC day.

A release that lacks an external URL is only matched by signature.

See [release-was-blocked.md](../troubleshooting/release-was-blocked.md) for how to inspect and clear blocklist entries.

---

### 2. Title match

The candidate title must contain the requested title as a word-prefix sequence. Matching is normalised:

- Punctuation (`.`, `-`, `_`, brackets) → space.
- Apostrophes and `!?` removed.
- `&` → `and`.
- Leading articles (`the`, `a`, `an`) stripped from both sides before re-testing.
- Up to one extra franchise-prefix word is allowed (e.g. `Marvels.Agents.of.SHIELD` matches `Agents of SHIELD`).

When the search used a direct ID (TMDB/TVDB/IMDb), `TrustSource=true` skips the title check for obfuscated NZB subjects. However, if the title string contains structured markers (year, resolution, `SxxExx`, streaming source), the check is applied anyway — a named title that says the wrong show is rejected even from a trusted ID search.

Alternative titles from TMDB (e.g. `Avengers Assemble` for UK markets) are also accepted.

Reject reason: `wrong_title`

---

### 3. Minimum age

If the quality profile sets `minimum_age_hours`, candidates posted more recently than that are rejected. This lets newly-posted releases propagate to all Usenet servers before being grabbed.

Reject reason: `too_new`

---

### 4. Profile exclude patterns

Free-text glob or substring patterns on the quality profile's `exclude_patterns` list are compiled as regular expressions and tested against the title. The first match rejects the candidate.

Reject reason: `excluded_pattern`

---

### 5. Hard rejections

These are unconditional and cannot be overridden by a release rule or custom format.

| Condition | Reject reason |
|---|---|
| Title contains CAM/TS/Screener/Workprint markers | `bad_source` |
| Title matches raw Blu-ray disc (BDMV, BD25/50/100, COMPLETE.BLURAY, BD-ISO) | `br_disk` |
| Title contains hardcoded subtitle markers (HC, SUBBED, HARDSUB) | `hardsub` |
| Size is outside MB/min limits (see quality definitions) | `too_small` or `too_large` |

CAM/TS detection uses word-boundary matching: ` ts `, `hdts`, `hd-ts`, etc. Single-letter tokens like `ts` require surrounding spaces or separator characters to avoid false positives on show titles.

---

### 6. Custom format scoring

All enabled custom formats are tested. Matching formats add or subtract points from both the total score and the `custom_format_score` subtotal. No candidate is rejected at this step — only scored.

See [custom-formats.md](custom-formats.md).

---

### 7. Release block rules

Block rules from `release_block_rules` are evaluated last. Rules with `action: block` cause immediate rejection. Rules with `action: penalty` subtract their `score_penalty` from the total score.

See [release-rules.md](release-rules.md).

---

## Scoring summary (base values)

After filtering, candidates are ranked by their total score. Higher is better. Key built-in score contributions:

| Signal | Points |
|---|---|
| Exact episode match | +350 |
| First preferred resolution | +500 |
| First preferred source | +300 |
| Named indexer | +75 |
| Proper/real (preferred) | +80 |
| Repack (preferred) | +60 |
| Release group detected | +50 |
| Remux | +40 |
| Proper/real (default) | +40 |
| Recent upload (<30 days) | +25 |
| Indexer trust score | score × 10 |
| Sample detected | -150 |
| Previous failure (×N) | -300 × N |
| 5+ prior failures | -50000 (effectively excluded) |

Resolution, source, codec, language, audio, and HDR scoring all use a preference-ordered list from the quality profile. The first item in the list scores highest (base − 0 × step); each subsequent item scores less. Items not in the list score negative (-120 by default).

---

## What happens after selection

The top non-rejected candidate becomes the selected release. The queue item transitions to `selected`, then moves through `fetching_nzb` → `indexing` → `preflight` → `publishing` → `available`.

If a failure occurs at any stage, the policy package classifies the reason and decides whether to:

- Blocklist the release and search for the next candidate.
- Search again without blocklisting.
- Retry later.
- Do nothing (wait for next scheduled cycle).

See [release-was-blocked.md](../troubleshooting/release-was-blocked.md) and [bad-release-picked.md](../troubleshooting/bad-release-picked.md).
