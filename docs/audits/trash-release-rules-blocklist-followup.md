# TRaSH / Release Rules / Blocklist Follow-up Audit

Date: 2026-06-14

## Scope

Rescan of the current Drakkar repository for:

- quality profiles and quality definitions
- custom formats
- release block rules
- runtime blocklist behavior
- ranking/scoring integration
- frontend/operator surfaces for these systems

This audit reflects the current codebase state, not the original task assumptions.

## 1. What currently exists

### Runtime/failure blocklist

- Runtime blocklist storage exists in `blocklist_items`.
- Runtime blocklist repository exists in [internal/database/blocklist_repository.go](/root/nzbproject/internal/database/blocklist_repository.go:1).
- Service boundary exists in [internal/blocklist/service.go](/root/nzbproject/internal/blocklist/service.go:1).
- API endpoints exist in [internal/api/router.go](/root/nzbproject/internal/api/router.go:685):
  - `GET /api/blocklist`
  - `GET /api/blocklist/stats`
  - `DELETE /api/blocklist/{id}`
  - `DELETE /api/blocklist?reason=...`
  - `DELETE /api/blocklist`
- Frontend blocklist management exists in [web/src/routes/settings/+page.svelte](/root/nzbproject/web/src/routes/settings/+page.svelte:67).

### Release filtering rules

- `release_block_rules` table exists via [migrations/000028_release_blocklist.sql](/root/nzbproject/migrations/000028_release_blocklist.sql:5).
- Repository CRUD exists in [internal/database/blocklist_rules_repository.go](/root/nzbproject/internal/database/blocklist_rules_repository.go:1).
- API CRUD and test endpoint exist in [internal/api/router.go](/root/nzbproject/internal/api/router.go:1320).
- Ranking integration exists in [internal/ranking/ranking.go](/root/nzbproject/internal/ranking/ranking.go:359).
- Frontend management exists in [web/src/routes/settings/+page.svelte](/root/nzbproject/web/src/routes/settings/+page.svelte:222).

### Custom formats

- `custom_formats` table exists via [migrations/000021_feature_pack.sql](/root/nzbproject/migrations/000021_feature_pack.sql:36).
- Repository CRUD exists in [internal/database/custom_formats_repository.go](/root/nzbproject/internal/database/custom_formats_repository.go:1).
- API CRUD exists in [internal/api/router.go](/root/nzbproject/internal/api/router.go:1272).
- Ranking integration exists in [internal/ranking/ranking.go](/root/nzbproject/internal/ranking/ranking.go:337).
- Frontend management exists in [web/src/routes/settings/+page.svelte](/root/nzbproject/web/src/routes/settings/+page.svelte:199).

### Quality profiles and definitions

- `quality_profiles` table exists from [migrations/000010_quality_profiles.sql](/root/nzbproject/migrations/000010_quality_profiles.sql:4) and later follow-up migrations.
- `quality_definitions` table exists from [migrations/000018_quality_profiles_v3.sql](/root/nzbproject/migrations/000018_quality_profiles_v3.sql:6).
- Quality definition size-column rename already exists in [migrations/000030_quality_defs_mb_per_minute.sql](/root/nzbproject/migrations/000030_quality_defs_mb_per_minute.sql:1).
- Repository/API exist in [internal/database/profile_repository.go](/root/nzbproject/internal/database/profile_repository.go:8) and [internal/api/router.go](/root/nzbproject/internal/api/router.go:1234).
- Ranking consumes profile-level and tier-level MB/min values in [internal/ranking/ranking.go](/root/nzbproject/internal/ranking/ranking.go:525).
- Workflow loads tier limits from `quality_definitions` in [internal/workflow/service.go](/root/nzbproject/internal/workflow/service.go:1992).

## 2. What is partially implemented

### Quality size semantics

- Backend semantics for `quality_definitions` are already MB/minute.
- Runtime-dependent checks already skip when runtime is unknown in [internal/ranking/ranking.go](/root/nzbproject/internal/ranking/ranking.go:529).
- `quality_definitions` UI in Settings is already labeled MB/min in [web/src/routes/settings/+page.svelte](/root/nzbproject/web/src/routes/settings/+page.svelte:1422).
- Profile-level API/type naming is now aligned to MB/minute:
  - Go JSON fields are `minMbPerMinute` / `maxMbPerMinute` in [internal/database/profile_repository.go](/root/nzbproject/internal/database/profile_repository.go:12).
  - Legacy `minSizeMb` / `maxSizeMb` payloads are still accepted during JSON decode for compatibility.
  - Frontend types and forms use the MB/minute names in [web/src/lib/types.ts](/root/nzbproject/web/src/lib/types.ts:420), [web/src/routes/profiles/+page.svelte](/root/nzbproject/web/src/routes/profiles/+page.svelte:1), and [web/src/routes/settings/+page.svelte](/root/nzbproject/web/src/routes/settings/+page.svelte:1).
- `quality_profiles` storage naming is now also aligned to MB/minute via the follow-up migration.

### Runtime blocklist UI/API cleanup

- Pagination, filtering, stats and reason-clearing are already implemented.
- The system is cleaner than the pasted task assumed.
- The blocklist page and API now expose enriched metadata when it can be derived safely:
  - `keyType`
  - `releaseTitle`
  - `indexerName`
  - `sizeBytes`
  - `postedAt`
  - `libraryItemId`
  - `selectedReleaseId`
- The Settings UI now distinguishes runtime keys from matched release context and adds safer clear/copy actions.

### Release filtering vs runtime blocklist separation

- The systems are distinct in code and conceptually mostly aligned:
  - runtime blocklist in `internal/blocklist` + workflow repository methods
  - user filtering in `release_block_rules`
  - user scoring in `custom_formats`
- The UI already splits them into separate Settings tabs.
- Candidate explanations now expose selected/rejected/failure/archive context in release responses. Further polish is optional, not a missing baseline capability.

## 3. What is missing

### Audit/task assumptions that are no longer accurate

- Phase 2 from the pasted task is not fully missing. A quality-definition rename migration and MB/minute runtime logic already exist.
- The current repo does not need a fresh `quality_definitions` migration for MB/minute columns unless compatibility cleanup is required.

### Remaining real gaps

- Queue workflows have selected-item bulk actions now; only deeper Servarr-style polish remains optional.
- Seerr season-scoped requests can now be issued from TV detail views for missing seasons, but true per-episode Seerr request creation is still blocked by the upstream Seerr API, which only accepts full-show or season arrays on `POST /request`.
- Repair handling is now partially operator-exposed via a callable Deep NZB Article Check task/API that resets missing-article or sample-only publications for re-queue. True PAR2 repair/reconstruction remains unimplemented.
- Local operator auth is now materially complete for the original task scope: setup/login, multi-user management, and personal API tokens are all present. Remaining auth work would be additive rather than baseline.

## 4. Where the current blocklist is implemented

### Backend

- Service boundary: [internal/blocklist/service.go](/root/nzbproject/internal/blocklist/service.go:1)
- Repository: [internal/database/blocklist_repository.go](/root/nzbproject/internal/database/blocklist_repository.go:1)
- Blocklist key generation and release-signature matching:
  - [internal/database/workflow_repository.go](/root/nzbproject/internal/database/workflow_repository.go:1733)
  - [internal/database/workflow_repository.go](/root/nzbproject/internal/database/workflow_repository.go:1759)
- Queue/retry policy integration:
  - [internal/workflow/service.go](/root/nzbproject/internal/workflow/service.go:568)
  - [internal/workflow/service.go](/root/nzbproject/internal/workflow/service.go:1162)

### API

- [internal/api/router.go](/root/nzbproject/internal/api/router.go:685)

### Frontend

- API client: [web/src/lib/api.ts](/root/nzbproject/web/src/lib/api.ts:73)
- Types: [web/src/lib/types.ts](/root/nzbproject/web/src/lib/types.ts:351)
- Settings tab UI: [web/src/routes/settings/+page.svelte](/root/nzbproject/web/src/routes/settings/+page.svelte:67)

## 5. Where `release_block_rules` are implemented

- Migration and default seeds: [migrations/000028_release_blocklist.sql](/root/nzbproject/migrations/000028_release_blocklist.sql:5)
- DB model: [internal/database/models.go](/root/nzbproject/internal/database/models.go:313)
- Repository: [internal/database/blocklist_rules_repository.go](/root/nzbproject/internal/database/blocklist_rules_repository.go:1)
- Workflow loader: [internal/workflow/service.go](/root/nzbproject/internal/workflow/service.go:2062)
- Ranking application: [internal/ranking/ranking.go](/root/nzbproject/internal/ranking/ranking.go:359)
- Test endpoint logic: [internal/ranking/ranking.go](/root/nzbproject/internal/ranking/ranking.go:403)
- API endpoints: [internal/api/router.go](/root/nzbproject/internal/api/router.go:1320)
- Frontend settings UI: [web/src/routes/settings/+page.svelte](/root/nzbproject/web/src/routes/settings/+page.svelte:222)

## 6. Where `custom_formats` are implemented

- Migration: [migrations/000021_feature_pack.sql](/root/nzbproject/migrations/000021_feature_pack.sql:36)
- DB model: [internal/database/models.go](/root/nzbproject/internal/database/models.go:305)
- Repository: [internal/database/custom_formats_repository.go](/root/nzbproject/internal/database/custom_formats_repository.go:1)
- Workflow loader: [internal/workflow/service.go](/root/nzbproject/internal/workflow/service.go:2044)
- Ranking application: [internal/ranking/ranking.go](/root/nzbproject/internal/ranking/ranking.go:337)
- API endpoints: [internal/api/router.go](/root/nzbproject/internal/api/router.go:1272)
- Frontend settings UI: [web/src/routes/settings/+page.svelte](/root/nzbproject/web/src/routes/settings/+page.svelte:199)

## 7. Where ranking/scoring is implemented

- Main scoring engine: [internal/ranking/ranking.go](/root/nzbproject/internal/ranking/ranking.go:174)
- Size rejection: [internal/ranking/ranking.go](/root/nzbproject/internal/ranking/ranking.go:525)
- Workflow candidate construction and scoring:
  - [internal/workflow/service.go](/root/nzbproject/internal/workflow/service.go:300)
  - [internal/workflow/service.go](/root/nzbproject/internal/workflow/service.go:1210)
  - [internal/workflow/service.go](/root/nzbproject/internal/workflow/service.go:1910)
- Candidate persistence with reject reasons/scores:
  - [internal/database/workflow_repository.go](/root/nzbproject/internal/database/workflow_repository.go:846)

## 8. How current failed-release blocklist and release filtering should connect

### Current actual boundary

- Runtime/failure blocklist:
  - blocks failed URLs and release signatures
  - stores transient/durable operational failures
  - is used to prevent retrying known bad releases
- Release block rules:
  - user-managed global filtering rules
  - reject or penalize by release group, title pattern, regex, or missing release group
- Custom formats:
  - user-managed additive scoring rules

### Recommended boundary

- Keep runtime blocklist in `internal/blocklist` + workflow repository.
- Keep release filtering inside ranking inputs as `BlockRules`.
- Keep custom formats inside ranking inputs as additive score modifiers.
- Candidate evaluation order should remain:
  1. skip runtime-blocklisted candidates before ranking or via persisted reject state
  2. apply hard parser/rule rejects
  3. apply release block rules
  4. apply custom format scoring
  5. persist score and reject reason

### Main cleanup still needed

- Optional refinement only:
  - even richer score diagnostics if operators later want per-component machine-readable breakdowns instead of persisted explanation strings
  - richer queue-selection UX beyond the current selected-failed bulk actions if more parity is needed

## 9. Migration risks

### Low risk

- Further explanatory UI tweaks.
- Writing audit and docs only.

### Medium risk

- Further expanding blocklist item schema with persisted denormalized release metadata:
  - requires deciding whether metadata is snapshotted at insert time or derived later
  - derived joins may fail once source rows are deleted or changed

### High risk

- Creating a second manual blocklist model without clear semantics.
- Merging runtime blocklist and release rules into one table or one UI concept.

## 10. Exact implementation plan

### Phase A: finish the audit-aligned cleanup first

1. Keep `quality_definitions` as-is; do not add another MB/minute migration there.
2. Keep MB/minute semantics consistent through DB, API, and UI.
3. Add explicit UI/help text:
   - profile limits are MB/minute
   - checks only apply when runtime metadata is known

### Phase B: close the profile semantic leak

Status: completed for API/types/UI compatibility layer.

1. Profile API/type fields use `minMbPerMinute` / `maxMbPerMinute`.
2. Legacy `minSizeMb` / `maxSizeMb` payloads are still accepted during transition.
3. Frontend types and forms have been updated accordingly.
4. Additional ranking coverage remains optional if more edge cases are added later.

### Phase C: improve blocklist observability, not its core behavior

Status: completed for derived metadata enrichment.

1. Current runtime blocklist API behavior remains intact.
2. Enriched metadata is now returned when it can be derived safely:
   - `libraryItemId`
   - `selectedReleaseId`
   - `releaseTitle`
   - `indexerName`
   - `sizeBytes`
   - `postedAt`
   - `keyType`
3. Stored metadata columns are still unnecessary unless derived joins prove insufficient later.

### Phase D: explain the three systems more clearly

Status: completed for the current scope.

1. Settings descriptions now distinguish:
   - Runtime Blocklist = operational failures and temporary/durable runtime exclusions
   - Release Filtering = user block/penalty rules
   - Custom Formats = scoring rules
2. Profile editors now state that MB/minute checks are skipped when runtime metadata is unknown.
3. Release views now show candidate explanation text using current reject/failure/archive context.

### Phase E: only then consider optional schema work

1. Evaluate whether richer score-breakdown explanations are worth the added API weight.
2. Queue bulk actions for selected failed history rows now exist; only further parity polish should be evaluated.
3. Avoid any duplication of release rules into runtime blocklist tables.

## Summary

The current repo is ahead of the pasted task in several areas:

- runtime blocklist pagination/stats/filtering already exist
- release block rules already exist and are wired into ranking
- custom formats already exist and are wired into ranking
- `quality_definitions` already use MB/minute naming and logic

The main remaining work is now optional refinement:

- expand score-breakdown detail if operators need deeper ranking introspection
- add only further queue UX polish if the current selected-failed bulk actions are still not enough
