# Maintenance

## Scheduled tasks (Settings > Tasks tab; all automated, also runnable on demand)

- **Run Health Check** (15m) — verifies published symlinks are reachable;
  surfaces "Consistency Issues" (an item marked available with no published
  symlink) on the Health page.
- **Deep NZB Article Check** (168h / weekly) — detects missing first/last
  NNTP articles and sample-only publications; resets affected items to
  re-queue. `POST /api/maintenance/nzb-health-check`.
- **Article Health Check** (6h) — lighter-weight article reachability sweep.
- **Storage Maintenance** (6h) — merged cache-prune + library-cleanup pass:
  orphaned content, stale publication metadata, expired blocklist/
  auth-token/recent-URL-fetch rows.
- **Content Maintenance** (6h) — merged fill-missing-episodes + search-
  upgrades pass: creates library items for episodes TMDB knows about that
  aren't tracked yet, and re-checks items whose quality profile allows
  upgrading to a better release.
- **Publishing Maintenance** (30m) — republish/consistency-repair pass.

## Health page manual actions

- `POST /api/library/republish-pending` — republish every item flagged
  pending-republish (recoverable: has a matching virtual file, just missing
  the symlink).
- `POST /api/library/reset-orphaned-available` — reset items that are
  available but genuinely unrecoverable (no matching virtual file at all)
  back to `requested` so they re-enter the normal search cycle.

Both read from `ListPendingRepublishTargets`/`ListUnrecoverableLibraryItems`
respectively — an item only shows as a stuck "Consistency Issue" if it's
`available=true` with no `symlink_publications` row; which bucket it falls
into (recoverable vs. not) depends on whether a matching virtual file can
still be found for it.

Maintenance cursor/last-run state lives in `maintenance_cursors`.
