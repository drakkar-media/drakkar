# Virtual Filesystem Mount

Drakkar itself does not mount FUSE — it serves the virtual filesystem over
WebDAV (`internal/dav`). The `rclone` container mounts that WebDAV endpoint
as a real FUSE mount, and only at `/mnt/drakkar/vfs`.

Forbidden mount targets for that rclone mount (would create a loop or mask
real host paths):

- `/mnt/drakkar`
- `/mnt/drakkar/media`
- `/mnt/drakkar/media/movies`
- `/mnt/drakkar/media/tv`

Host-side library symlinks (published under `/mnt/drakkar/media/movies` and
`/mnt/drakkar/media/tv`) point *into* `/mnt/drakkar/vfs/content/...` — the
media library paths themselves stay outside the FUSE mount, per
[docker-compose.md](docker-compose.md).
