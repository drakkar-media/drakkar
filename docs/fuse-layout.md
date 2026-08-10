# Virtual Filesystem Layout

There is no in-process kernel FUSE driver (no `internal/fuse` package) —
the virtual filesystem is served over WebDAV (`internal/dav`), and `rclone`
mounts that WebDAV endpoint locally at `/mnt/drakkar/vfs` to present it as a
real FUSE mount to the host and to Plex/Jellyfin.

Top-level virtual directories (`internal/dav/handler.go`):

- `content` — the actual media files backing every selected release,
  organized by `releases/<release-id>/...`. This is what host-side library
  symlinks point into (`internal/library`), and where playback reads
  actually resolve to Usenet segments on demand.
- `completed-symlinks` — lists persisted publication symlinks
  (`symlink_publications`) and resolves their targets.
- `nzbs` — active NZB documents from PostgreSQL:
  - reading `/nzbs/<name>.nzb` returns the original XML bytes
  - unlinking `/nzbs/<name>.nzb` cancels the matching queue item
  - create/write/flush/release stages an uploaded NZB into
    `/mnt/drakkar/staging/nzbs` and imports it on flush/release
- `.ids` — synthetic identifier mapping used internally by the handler.

Synthetic timestamp used for entries with no real mtime:
`2000-01-01T00:00:00Z`.
