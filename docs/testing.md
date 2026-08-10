# Testing

## Running tests

Most packages run standalone (`go test ./...`), but anything touching
`internal/database` (repository methods) or `internal/workflow`/`internal/api`
tests that exercise a repository skips itself unless a real Postgres is
available:

```
export DRAKKAR_TEST_DATABASE_URL=postgres://drakkar:test@localhost:5432/drakkar_test?sslmode=disable
go test ./...
```

Migrations apply automatically the first time a test opens that database,
or run them explicitly via `go run ./cmd/migrate` (reads
`DRAKKAR_TEST_DATABASE_URL`, falls back to `DATABASE_URL`).

CI runs the full suite against a real Postgres service container — a test
that only passes because `DRAKKAR_TEST_DATABASE_URL` was unset locally (and
silently skips) will still be caught there.

Frontend: `cd web && npm run check` (svelte-check, type/unused-CSS-selector
linting) and `npm run build`.

## What's covered

Broad strokes, by area — see each package's `*_test.go` files for the
actual current list, this is not meant to be an exhaustive index:

- **Config**: `settings.json` parsing, secret redaction, startup path
  validation.
- **Virtual filesystem** (`internal/dav`): directory listing, `/nzbs`
  create/write/flush/release/unlink, `/content` release mapping,
  `/completed-symlinks` metadata.
- **Workflow**: Seerr request sync, NZBHydra2 search/ranking, candidate
  selection and fallback-on-failure, queue retry, season-pack episode
  fan-out (including double-episode filenames), per-item pause/resume,
  the stuck-queue dispatch-eligibility query.
- **NNTP/streaming**: yEnc decoding, multiline body parsing, connection
  pooling/limits, priority scheduler, provider fallback/retry, read-ahead
  scheduling and cancellation, range-to-segment mapping.
- **Cache**: bounded in-memory LRU, disk cache put/get/trim, singleflight
  dedupe.
- **Archive**: RAR4/RAR5 header parsing, span-coverage validation.
- **Library/publishing**: host-side symlink publication, movie/TV path
  generation, startup publication reconstruction, republish endpoints.
- **Subtitles**: per-language dedup, provider rotation, daily budget
  enforcement, candidate ranking, zip-bundle extraction.
- **API**: every HTTP handler with a meaningful branch (auth, queue
  actions, health, settings, SABnzbd-compatible endpoints).

## Conventions worth following

- **Adversarial verification for correctness-critical fixes**: revert the
  fix, confirm the new test actually fails with the expected symptom,
  restore the fix, confirm it passes. A test that passes against both the
  buggy and fixed code isn't testing the fix.
- Prefer a real Postgres test DB over mocking the database — this project
  was burned before by mocked-DB tests passing while the real migration/
  query broke in production.
- Scratch verification programs (one-off `cmd/*_scratch/main.go` used to
  probe a real bug) are deleted before committing, never left in the repo.
