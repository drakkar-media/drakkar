# A bad release was picked

Drakkar picked a release you did not want — wrong resolution, wrong group, wrong size, or something else. This document explains how to investigate and correct it.

## Step 1: Check the scoring explanations

Every evaluated release candidate has an `explanations` list that shows every scoring contribution. In the UI, open the library item and expand the release candidates panel. Look at both the selected release and the other candidates.

Each explanation line shows what contributed and by how much:

```
Exact year match (+90)
Resolution 1080p (+400)
Source WEB-DL (+250)
Codec h264 (+120)
Audio TrueHD (+160)
Named indexer bonus (+75)
Release group detected (+50)
Custom format Dolby Vision (+150)
```

The candidate with the highest total score is selected.

## Step 2: Understand why the better release scored lower

Common reasons:

### Resolution or source not in the profile

If the profile's `resolutions` list does not include a resolution, it scores -120 instead of the built-in default. Check Settings > Quality Profiles and confirm the preferred resolutions are listed in order.

### Release group blocked or penalised

A release rule may be applying a penalty. Use the test endpoint:
```
POST /api/release-block-rules/test
{"title": "Movie.2024.2160p.UHD.BluRay.Remux.DV.TrueHD-GROUPNAME", "mediaType": "movie"}
```

### Custom format scoring difference

If you have custom formats that apply to one release but not another, check which formats matched. The `customFormatScore` subtotal is shown separately on each candidate.

### Prior failure penalty

A release that previously failed download gets a score penalty:
- 1–4 failures: -300 per failure
- 5+ failures: -50,000 (effectively excluded)

If the better-quality release was tried before and failed, it ends up with a large penalty. Check the candidate's `failureCount` and `lastFailureReason`.

### Blocklist hiding a better release

The better release may be in the blocklist from a previous failure. Check the blocklist for the release title. If the failure was transient (e.g. a temporary indexer outage), clear the entry and trigger a new search.

## Step 3: Force a different release

### Use the "Grab" button

In the release candidates list, find the release you want and click Grab. This immediately selects that candidate and starts the download workflow.

### Skip the current selection

If you want to move to the next-best candidate without manually choosing, click Skip on the selected release. The next non-rejected candidate in score order is selected automatically.

### Reject a candidate

To permanently reject a candidate for this item (it will not be auto-selected again), click Reject. You can restore rejected candidates later if needed.

## Step 4: Adjust scoring for the future

If the same pattern keeps causing the wrong release to be picked:

### Add or adjust a release rule

To consistently block a release group: Settings > Quality > Release Rules > Add Rule. Type: `release_group`, action: `block`.

To penalise rather than block: action `penalty`, set `score_penalty` to the point difference you want to overcome.

### Adjust the quality profile

If the profile's resolution or source order is wrong, fix it in Settings > Quality Profiles. The order matters — first item = most preferred.

### Add a custom format

To boost or penalise releases with a specific title pattern (e.g. `IMAX Enhanced`, `AI Upscale`), add a custom format with a positive or negative score.

### Set a cutoff resolution

If you only want upgrades up to a certain resolution, set `cutoff_resolution` on the profile. Once a release at or above cutoff is available, the upgrade scheduler skips the item.

## Common misconfigurations

**720p selected over 1080p:** The profile's `resolutions` list may not include `1080p`, or `720p` is listed first. Fix the order.

**CAM/TS not rejected:** The release has a title that does not trigger the CAM/TS detection patterns (e.g. it uses an unusual separator). Add a release rule with `type: regex` and a pattern like `(?i)\bHD[-._]?CAM\b`.

**A very old release is preferred over a new one:** Older releases accumulate grab counts which add score (up to +50). A trusted release with many community grabs may edge out a newer one. If recency matters more, add a custom format that boosts recent releases (e.g. match the current year in the title).

**Proper/repack is not preferred:** Check that `prefer_proper` and `prefer_repack` are enabled on the quality profile. With `prefer_proper=true`, a PROPER release gets +80 instead of +40.
