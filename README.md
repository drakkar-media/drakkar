# Drakkar

Drakkar is a self-hosted Usenet media downloader that integrates with Radarr, Sonarr, and Jellyfin/Plex. It streams NZB content on demand through a virtual filesystem — files appear immediately and data is fetched from Usenet only when read.

## How it works

- Requests flow in from Overseerr/Jellyseerr → search via NZBHydra2 → NZB is fetched and indexed
- Virtual media files appear in the FUSE filesystem at `/mnt/drakkar/media/` and are symlinked to your Radarr/Sonarr library paths
- Article data is streamed from Usenet providers on read, cached to disk; no full download required before playback begins
- Exposes a SABnzbd-compatible API so Radarr and Sonarr treat Drakkar as a regular download client

## Requirements

- Docker and Docker Compose
- A Usenet provider with SSL access (NNTP port 563)
- [NZBHydra2](https://github.com/theotherp/nzbhydra2) for NZB indexing
- [Overseerr](https://overseerr.dev) or [Jellyseerr](https://github.com/Fallenbagel/jellyseerr) for media requests
- Radarr and/or Sonarr (optional, for library management)
- Plex or Jellyfin (optional, for playback)
- TMDB API key (optional, for metadata enrichment)

## Quick start

```bash
git clone https://github.com/hjongedijk/drakkar
cd drakkar
cp data/settings.example.json data/settings.json
chmod 600 data/settings.json
# Edit data/settings.json — add your Usenet provider, NZBHydra2, and Overseerr credentials
docker compose up -d
```

The web UI is available at `http://localhost:8080`.

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
| `metadata.tmdb` | TMDB API key for title/poster enrichment |
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

### Filesystem layout

```
/mnt/drakkar/
  media/
    movies/   ← symlinks to virtual movie files
    tv/       ← symlinks to virtual TV episode files
  vfs/        ← rclone-mounted WebDAV (backing store for FUSE reads)
  cache/
    blocks/   ← decoded Usenet article cache
```

## Features

- **Streaming reads** — Usenet articles are fetched on demand; no full download before playback
- **Read-ahead** — sequential reads trigger parallel segment prefetch to maintain playback throughput
- **Multi-provider fallback** — retries across multiple Usenet providers when articles are missing
- **Automatic candidate fallback** — if an NZB fails to import, the next ranked result is tried automatically
- **SABnzbd-compatible API** — drop-in replacement for Radarr/Sonarr download clients
- **SAB-compatible aliases** — available at `/sabnzbd/api`, `/api/sabnzbd/api`, and `/dav/api`
- **Quality profiles** — configurable resolution/codec preferences with cutoff support
- **Custom formats** — regex-based release scoring on top of quality profiles
- **Subtitle integration** — automatic search and download via SubDL and OpenSubtitles
- **Notifications** — Discord webhook and generic HTTP webhook on grab/import/failure events
- **WebDAV server** — built-in WebDAV for rclone to serve as the FUSE backing store

## Building from source

```bash
# Build frontend
cd web && npm install && npm run build && cd ..

# Build binary
go build -o bin/drakkar ./cmd/drakkar

# Or build the Docker image
docker build -t drakkar .
```

## License

MIT — see [LICENSE](LICENSE).
