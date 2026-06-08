# Maintenance

Implemented manual tasks:

- remove orphaned content
- remove broken media symlinks
- remove orphaned completed-history symlinks

HTTP endpoints:

- `POST /api/maintenance/orphaned-content`
- `POST /api/maintenance/broken-media-symlinks`
- `POST /api/maintenance/orphaned-completed-symlinks`

Each task returns a `maintenance.Result` payload with scanned and deleted row/file counts.

Maintenance state belongs in `maintenance_cursors`.
