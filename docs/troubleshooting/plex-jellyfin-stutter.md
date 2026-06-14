# Plex / Jellyfin stutter and playback issues

Drakkar streams video directly from Usenet over FUSE. Content is never fully downloaded before playback begins. This document covers why stutter happens and how to fix it.

## How streaming works

When Plex or Jellyfin opens a file from the Drakkar VFS mount, reads go through a three-tier priority NNTP scheduler:

- **Interactive (priority 100):** the player's current read request — served immediately.
- **Read-ahead (priority 80):** speculative prefetch of the next ~512 MB window behind the playhead.
- **Background (priority 10):** calibration, preflight checks — runs only when no streaming is active.

The scheduler always drains interactive requests before read-ahead, and read-ahead before background. Interactive reads should never be delayed by background work.

Read-ahead is distributed across all active streams. With two simultaneous streams, each gets half the allocated read-ahead parallelism, with a floor of 1 parallel connection per session.

## Key parameters

| Setting | Default | Where |
|---|---|---|
| `usenet.maxDownloadConnections` | 15 | settings.json |
| `usenet.streamingPriorityPercent` | 80 | settings.json |
| `usenet.articleBufferSize` | 40 | settings.json |
| `--read-ahead-limit-bytes` | 512 MB | runtime flag |
| `--disk-cache-limit-bytes` | 20 GB | runtime flag |

**Read-ahead parallelism** is computed as:

```
streamingBudget = totalConnections * (streamingPriorityPercent / 100)
readAheadParallelism = streamingBudget / 4
```

With defaults (15 connections, 80%): streaming budget = 12, read-ahead parallelism = 3. Minimum 1, maximum 15.

**Article buffer size** limits how many articles ahead are prefetched per session (default 40 articles). One article is typically ~700 KB decoded, so 40 articles ≈ 28 MB pre-fetched ahead.

## Diagnosing stutter

### Check active streams

```
GET /api/streams
```

Returns all open FUSE sessions with current byte offset. If multiple streams are active, read-ahead is split between them.

### Check metrics

The status API (`GET /api/status`) includes:
- `activeStreams` — current open sessions
- `readAheadCancellations` — how often a seek cancelled in-flight read-ahead (normal for skipping; high counts during normal playback indicate seek thrashing)

### Check NNTP connection count

If `maxDownloadConnections` is lower than what your provider allows, you are under-using the connection pool. The provider's connection limit is the ceiling; set `maxDownloadConnections` to match it.

### Check the disk cache

The decoded segment disk cache stores recently-fetched NNTP articles at `/mnt/drakkar/cache/blocks`. Its size limit is `--disk-cache-limit-bytes` (default 20 GB). Cache hits avoid re-fetching from Usenet on seeks and re-plays.

If the cache is too small for your typical file size (a 4K Remux can be 60+ GB), seeking backward or replaying a scene will re-fetch from Usenet. Increase the disk cache size if you have available disk space.

## Common causes and fixes

### Stutter at the start of playback

**Cause:** Plex is pre-buffering before signalling that playback started. Read-ahead has not warmed up yet.

**Fix:** This is normal for the first few seconds. If it persists more than 10-15 seconds, your provider connection speed may be too slow for the bitrate of the content. Check NNTP connection speeds in the Drakkar logs.

### Stutter when seeking

**Cause:** Seeking cancels the current read-ahead window and starts a new one at the seek position. The player gets an interactive fetch at the new offset, then read-ahead warms up again. A single stutter on seek is expected.

**Fix:** If stutter after seek is long, increase `maxDownloadConnections` and/or `--disk-cache-limit-bytes`. If the seek position is within the disk cache, there is no re-fetch at all.

### Continuous stutter during playback

**Cause:** NNTP download speed is below the content's bitrate.

**Fix:**
1. Increase `maxDownloadConnections` up to your provider's connection limit.
2. Add a backup Usenet provider to the provider list — Drakkar falls back to backup providers for missing articles and additional download capacity.
3. Reduce `streamingPriorityPercent` to give more connections to read-ahead (default 80% means 80% of connections are reserved for streaming, with 20% for read-ahead). Counter-intuitively, lowering this value reserves more connections for prefetch.

Wait — actually `streamingPriorityPercent` controls the streaming budget, and read-ahead is a quarter of that. To get more read-ahead parallelism, increase `maxDownloadConnections`.

### Stutter only with multiple simultaneous streams

**Cause:** Read-ahead parallelism is split across all active sessions. With 3 streams and parallelism of 3, each session gets 1 read-ahead connection.

**Fix:** Increase `maxDownloadConnections` to give each stream more capacity. The formula above shows how read-ahead parallelism scales.

### "File not found" or empty file in Plex

**Cause:** The FUSE mount is not running, or the symlink in the media library path is broken.

**Fix:** Check the FUSE mount status in the Drakkar UI (Dashboard > System). Verify the media library symlinks point into the FUSE mount at `/mnt/drakkar/vfs`.

### Plex/Jellyfin does not see the new file

**Cause:** Plex and Jellyfin need to scan the library directory to detect new symlinks.

**Fix:** Drakkar automatically triggers a library refresh via the Plex and Jellyfin APIs when a new file is published. If this is not happening:
- Confirm Plex is configured in Settings with a valid URL and token.
- Confirm Jellyfin is configured with a valid URL and API key.
- Check the application logs for `plex refresh` or `jellyfin refresh` entries.

Manually trigger a scan in the Plex or Jellyfin UI if automatic refresh is not working.

## Calibration and seek accuracy

On first access after a file is indexed, Drakkar calibrates the segment byte offsets by fetching the first and last segments from Usenet and measuring their actual decoded size. This corrects the estimated sizes from the NZB header.

Without calibration, seek positions are based on estimates and may be off by a few seconds. With calibration complete, seeks land accurately. Calibration runs in the background and is transparent to the player.

If a seek puts the player past the end of the file (Plex reports "end of stream" unexpectedly), calibration may not have corrected a size overestimate yet. Wait a minute and try again.
