# Docker Compose

Compose stack lives in repo root. App config comes from `./data/settings.json`, not `.env`.

Host mount rules:

- FUSE mount only at `/mnt/drakkar/vfs`
- media library paths stay outside FUSE
- `/mnt/drakkar` mount into container needs shared propagation
