# NZB Import

Parses NZB XML, computes decoded offset ranges per segment, and supports
both a file upload and a direct URL import path.

## Staged upload/import flow

1. Create virtual upload (file) or fetch (URL).
2. Stream/write into `/mnt/drakkar/staging/nzbs`.
3. Validate XML.
4. Persist the queue item.
5. Persist the NZB document, NZB files, and segment counts.
6. Advance queue state from `indexing` to `preflight`.

## Entry points

- `POST /api/nzbs/import` — upload an `.nzb` file directly (Downloads
  page "Upload NZB").
- `POST /api/nzbs/import-url` — import by pasting an NZB URL (Downloads
  page "Add NZB URL").
- `GET /api/nzbs` / `DELETE /api/nzbs/{id}` — list/cancel active imports.
- WebDAV `/nzbs` create/write/flush/release (see
  [fuse-layout.md](fuse-layout.md)) — the same import path, reachable as a
  virtual filesystem write instead of an API call.
