# Drakkar

[![License](https://img.shields.io/github/license/drakkar-media/drakkar)](LICENSE)
[![Release](https://img.shields.io/github/v/release/drakkar-media/drakkar)](https://github.com/drakkar-media/drakkar/releases)
[![Docker Image](https://img.shields.io/badge/ghcr.io-drakkar--media%2Fdrakkar-2496ED?logo=docker&logoColor=white)](https://github.com/drakkar-media/drakkar/pkgs/container/drakkar)

Drakkar is a self-hosted Usenet media manager for Plex and Jellyfin. It finds releases, streams them straight from Usenet through a virtual filesystem, and hands them off the moment they're requested — no full download sitting between a request and playback.

**Website:** [drakkar-media.github.io](https://drakkar-media.github.io)

## Highlights

- Stream-first virtual filesystem — playback starts before a file finishes downloading
- Multi-provider Usenet pool with automatic fallback
- Smart season-pack fulfillment — one search satisfies every requested episode
- Self-healing library — broken symlinks and stale releases are detected and repaired automatically
- SABnzbd-compatible API — drop-in download client for Radarr and Sonarr
- Native Overseerr/Jellyseerr request handling
- Quality profiles, custom formats, and automatic subtitle search

## Quick start

For a production deployment you only need two files — no repository clone required:

```bash
mkdir drakkar && cd drakkar
curl -O https://raw.githubusercontent.com/drakkar-media/drakkar/main/docker-compose.yml
mkdir -p data
curl -o data/settings.json https://raw.githubusercontent.com/drakkar-media/drakkar/main/data/settings.example.json
# edit data/settings.json — add your Usenet provider, NZBHydra2, and Overseerr credentials
docker compose up -d
```

The web UI is available at `http://localhost:8080`.

To upgrade later:

```bash
docker compose pull
docker compose up -d
```

## Requirements

- Docker and Docker Compose
- A Usenet provider with SSL access (NNTP port 563)
- [NZBHydra2](https://github.com/theotherp/nzbhydra2) for NZB indexing
- [Overseerr](https://overseerr.dev) or [Jellyseerr](https://github.com/Fallenbagel/jellyseerr) for media requests
- Radarr and/or Sonarr (optional, for library management)
- Plex or Jellyfin (optional, for playback)
- TMDB API key (optional, for metadata enrichment)

## How it works

- Requests flow in from Overseerr/Jellyseerr → search via NZBHydra2 → NZB is fetched and indexed
- Virtual media files appear in the FUSE filesystem at `/mnt/drakkar/media/` and are symlinked to your Radarr/Sonarr library paths
- Article data is streamed from Usenet providers on read, cached to disk; no full download required before playback begins
- Exposes a SABnzbd-compatible API so Radarr and Sonarr treat Drakkar as a regular download client

## Runtime layout

```
/mnt/drakkar/
  media/
    movies/   ← symlinks to virtual movie files
    tv/       ← symlinks to virtual TV episode files
  vfs/        ← rclone-mounted WebDAV (backing store for FUSE reads)
  cache/
    blocks/   ← decoded Usenet article cache

data/
  settings.json     ← main configuration (see below)
  postgres/         ← PostgreSQL data
  valkey/           ← Valkey data
  rclone/           ← rclone.conf (generated automatically on first boot)
```

## Configuration

All configuration lives in `data/settings.json`. Copy `data/settings.example.json` for the full schema with all available fields.

Key sections:

| Section | Purpose |
|---|---|
| `database` | PostgreSQL connection (defaults match docker-compose) |
| `valkey` | Valkey/Redis connection (defaults match docker-compose) |
| `usenet.providers` | One or more NNTP providers — host, port, credentials, max connections |
| `nzbhydra2` | NZBHydra2 URL and API key |
| `seerr` | Overseerr/Jellyseerr URL and API key |
| `metadata.tmdb` / `metadata.tvdb` | Metadata provider API keys for title/poster enrichment |
| `subtitles` | SubDL and OpenSubtitles provider credentials |
| `library` | Override default symlink paths for movies and TV |

### Radarr / Sonarr setup

Add Drakkar as a download client using the SABnzbd protocol:

- **Host**: your Drakkar host
- **Port**: `8080`
- **API key**: any non-empty string (Drakkar accepts all keys)
- **Category**: `movies` for Radarr, `tv` for Sonarr

Set the remote path mapping so Radarr/Sonarr can find completed downloads:

| Remote path | Local path |
|---|---|
| `/mnt/drakkar/media/movies` | your Radarr root folder |
| `/mnt/drakkar/media/tv` | your Sonarr root folder |

## Features

- **Streaming reads** — Usenet articles are fetched on demand; no full download before playback
- **Read-ahead** — sequential reads trigger parallel segment prefetch to maintain playback throughput
- **Multi-provider fallback** — retries across multiple Usenet providers when articles are missing
- **Automatic candidate fallback** — if an NZB fails to import, the next ranked result is tried automatically
- **Season-pack fulfillment** — a single season-pack download satisfies every requested episode instead of searching each one individually
- **Self-healing library** — periodic health checks repair missing symlinks and stale releases, then notify Plex/Jellyfin
- **SABnzbd-compatible API** — drop-in replacement for Radarr/Sonarr download clients
- **SAB-compatible aliases** — available at `/sabnzbd/api`, `/api/sabnzbd/api`, and `/dav/api`
- **Quality profiles** — configurable resolution/codec preferences with cutoff support
- **Custom formats** — regex-based release scoring on top of quality profiles
- **Subtitle integration** — automatic search and download via SubDL and OpenSubtitles
- **Notifications** — Discord webhook and generic HTTP webhook on grab/import/failure events
- **WebDAV server** — built-in WebDAV for rclone to serve as the FUSE backing store

## Docker

Public images are published to:

- `ghcr.io/drakkar-media/drakkar`

Build it yourself:

```bash
docker build -t drakkar .
```

## Development

```bash
# Build frontend
cd web && npm install && npm run build && cd ..

# Build binary
go build -o bin/drakkar ./cmd/drakkar
```

## Contributing

Contributions are welcome.

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/my-change`
3. Commit your changes and open a Pull Request

Please keep contributions focused and describe clearly what your change improves.

## License

MIT — see [LICENSE](LICENSE).

## Disclaimer

This project is provided as-is, without warranty of any kind. The author(s) are not responsible for any damage, data loss, misconfiguration, or security issues resulting from its use.

Drakkar does not ship with movies, shows, subtitles, or indexer content. Do not use this project to infringe copyright, pirate media, or violate the laws or service terms that apply in your country or to the systems you connect it to.
