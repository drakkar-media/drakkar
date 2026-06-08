# Drakkar Settings Snapshot — 2026-06-05

Working configuration confirmed: 4K + 1080p playback via Plex, clean multi-segment reads at 12 MB/s.

## settings.json (non-secret fields)

| Field | Value |
|-------|-------|
| `database.host` | `postgres` |
| `database.name` | `drakkar` |
| `database.username` | `drakkar` |
| `valkey.host` | `valkey` |
| `valkey.port` | `6379` |
| `nzbhydra2.url` | `http://192.168.10.11:5076/api` |
| `seerr.url` | `http://192.168.10.11:5055` |
| `usenet.providers[0].name` | `Newshosting` |
| `usenet.providers[0].host` | `news.newshosting.com` |
| `usenet.providers[0].port` | `563` |
| `usenet.providers[0].tls` | `true` |
| `usenet.providers[0].maxConnections` | `30` |
| `subtitles.languages` | `["nl", "en"]` |
| `subtitles.providers.subdl.enabled` | `true` |
| `subtitles.providers.opensubtitles.enabled` | `true` |

## Key compiled-in constants

| Constant | Value | File |
|----------|-------|------|
| `readAheadParallelism` | **40** | `internal/stream/session_manager.go` |
| `estimateDecodedSize` factor | **0.97** | `internal/nzb/parser.go` |
| `DefaultDiskCacheLimitBytes` | 20 GiB | `internal/config/config.go` |
| `DefaultReadAheadLimitBytes` | 512 MiB | `internal/config/config.go` |
| `DefaultMemoryHotCacheBytes` | 512 MiB | `internal/config/config.go` |
| `MaxWrite / MaxReadAhead` (FUSE) | 4 MiB (kernel caps at 1 MiB) | `internal/fuse/mount.go` |

## Actual measured yEnc segment sizes (Newshosting / this uploader)
- Encoded per segment: ~739,460 bytes
- Decoded per segment: **716,800 bytes** (ratio 0.969)
- Segment boundaries: **uniform** — k × 716,800 per index k
- Calibration runs at startup (`db.CalibrateAllNZBOffsets`) and on each new NZB import

## Deployment
- Docker Compose: `/root/nzbproject/docker-compose.yml`
- Image: `ghcr.io/hjongedijk/drakkar:latest`
- Frontend: embedded in Go binary (`adapter-static`, `internal/frontend/build/`)
- Build command: `make build && docker build -t ghcr.io/hjongedijk/drakkar:latest .`
- Migrate + restart: `docker compose up -d drakkar`
