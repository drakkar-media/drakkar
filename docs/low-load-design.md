# Low-Load Design

Controls that keep Drakkar's steady-state CPU/memory/connection footprint
small even under a large library and heavy background scan activity:

- Bounded NNTP connection pool per provider (`maxConnections`), reused
  across requests rather than opened per-fetch.
- A priority scheduler in front of the pool: interactive playback reads
  jump ahead of read-ahead and background fetches instead of contending
  equally for the same connections.
- Singleflight dedupe on concurrent block fetches — multiple readers
  wanting the same segment range trigger exactly one NNTP fetch.
- Bounded in-memory LRU plus a bounded on-disk spill cache for decoded
  article bodies (`readAheadLimitBytes` / `diskCacheLimitBytes` /
  `memoryHotCacheBytes`, all runtime-configurable).
- Bounded read-ahead window with cancellation on seek/close, so an
  abandoned stream doesn't keep fetching segments nobody will read.
- Scheduled background tasks (search, retry, health checks) run on fixed
  intervals with their own concurrency caps and cooldowns (e.g. NZBHydra2
  request pacing, escalating cooldown after a `429`) rather than as tight
  loops, and explicitly skip themselves under detected resource pressure
  (see `runStorageMaintenance`/`runContentMaintenance` "skipping non-
  critical task" logging in `internal/app`).
- Strict path-separation validation at startup (mount layout, writable
  host paths) catches a misconfiguration before it causes runtime damage.
