# NZB Import

Current code parses NZB XML, computes decoded offset ranges per segment, and supports staged manual import.

Implemented staged import behavior:

1. create virtual upload
2. stream writes to `/mnt/drakkar/staging/nzbs`
3. validate XML on flush/release
4. persist queue item
5. persist NZB document, NZB files and segment counts
6. advance queue state from `indexing` to `preflight`

Implemented entry points:

- `POST /api/nzbs/import`
- FUSE `/nzbs` create/write/flush/release

Still pending:

- richer `/nzbs` restart reconstruction tests against mounted kernel FUSE
- more precise errno mapping for every partial-upload failure mode
