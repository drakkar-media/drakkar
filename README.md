<div align="center">

# ⚡ Drakkar

**A self-hosted Usenet media manager for Plex and Jellyfin.**

Finds releases, streams them straight from Usenet through a virtual filesystem, and hands them off the moment they're requested — no full download sitting between a request and playback.

[![License](https://img.shields.io/github/license/drakkar-media/drakkar?color=2eeace)](LICENSE)
[![Release](https://img.shields.io/github/v/release/drakkar-media/drakkar?color=2eeace)](https://github.com/drakkar-media/drakkar/releases)
[![Docker Image](https://img.shields.io/badge/ghcr.io-drakkar--media%2Fdrakkar-2496ED?logo=docker&logoColor=white)](https://github.com/drakkar-media/drakkar/pkgs/container/drakkar)
[![Website](https://img.shields.io/badge/website-drakkar--media.github.io-9146FF)](https://drakkar-media.github.io)

</div>

---

## 📖 Table of contents

- [✨ Highlights](#-highlights)
- [🚀 Quick start](#-quick-start)
- [✅ Requirements](#-requirements)
- [⚙️ How it works](#️-how-it-works)
- [🗂️ Runtime layout](#️-runtime-layout)
- [🔧 Configuration](#-configuration)
- [🧩 Features](#-features)
- [🐳 Docker](#-docker)
- [🧑‍💻 Development](#-development)
- [🤝 Contributing](#-contributing)
- [📄 License](#-license)
- [⚠️ Disclaimer](#️-disclaimer)

---

## ✨ Highlights

- 🎬 **Stream-first virtual filesystem** — playback starts before a file finishes downloading
- 🔁 **Multi-provider Usenet pool** with automatic fallback
- 📦 **Smart season-pack fulfillment** — one search satisfies every requested episode
- 🩹 **Self-healing library** — broken symlinks and stale releases are detected and repaired automatically
- 🙋 **Native [Seerr](https://seerr.dev) request handling**
- 🎯 **Quality profiles, custom formats, and automatic subtitle search**

## 🚀 Quick start

For a production deployment you only need two files — no repository clone required:

```bash
mkdir drakkar && cd drakkar
curl -O https://raw.githubusercontent.com/drakkar-media/drakkar/main/docker-compose.yml
mkdir -p data
curl -o data/settings.json https://raw.githubusercontent.com/drakkar-media/drakkar/main/data/settings.example.json
# edit data/settings.json — add your Usenet provider, NZBHydra2, and Seerr credentials
docker compose up -d
```

> [!TIP]
> The web UI is available at `http://localhost:8080` once the stack is healthy.

To upgrade later:

```bash
docker compose pull
docker compose up -d
```

## ✅ Requirements

- Docker and Docker Compose
- A Usenet provider with SSL access (NNTP port 563)
- [NZBHydra2](https://github.com/theotherp/nzbhydra2) for NZB indexing
- [Seerr](https://seerr.dev) for media requests
- Plex or Jellyfin (optional, for playback)
- TMDB API key (optional, for metadata enrichment)

## ⚙️ How it works

1. A request comes in from **Seerr**
2. Drakkar searches via **NZBHydra2** and ranks the results by quality profile
3. The winning NZB is fetched and indexed — no article bodies downloaded yet
4. A virtual media file appears instantly in the FUSE filesystem and is symlinked into your Plex/Jellyfin library
5. Article data streams from Usenet on read, cached to disk — playback starts immediately, no full download required

## 🗂️ Runtime layout

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

## 🔧 Configuration

All configuration lives in `data/settings.json`. Copy `data/settings.example.json` for the full schema with all available fields.

Key sections:

| Section | Purpose |
|---|---|
| `database` | PostgreSQL connection (defaults match docker-compose) |
| `valkey` | Valkey/Redis connection (defaults match docker-compose) |
| `usenet.providers` | One or more NNTP providers — host, port, credentials, max connections |
| `nzbhydra2` | NZBHydra2 URL and API key |
| `seerr` | [Seerr](https://seerr.dev) URL and API key |
| `metadata.tmdb` / `metadata.tvdb` | Metadata provider API keys for title/poster enrichment |
| `subtitles` | SubDL and OpenSubtitles provider credentials |
| `library` | Override default symlink paths for movies and TV |

## 🧩 Features

- **Streaming reads** — Usenet articles are fetched on demand; no full download before playback
- **Read-ahead** — sequential reads trigger parallel segment prefetch to maintain playback throughput
- **Multi-provider fallback** — retries across multiple Usenet providers when articles are missing
- **Automatic candidate fallback** — if an NZB fails to import, the next ranked result is tried automatically
- **Season-pack fulfillment** — a single season-pack download satisfies every requested episode instead of searching each one individually
- **Self-healing library** — periodic health checks repair missing symlinks and stale releases, then notify Plex/Jellyfin
- **SABnzbd-compatible API** — available at `/sabnzbd/api`, `/api/sabnzbd/api`, and `/dav/api` for tools that speak the SABnzbd protocol
- **Quality profiles** — configurable resolution/codec preferences with cutoff support
- **Custom formats** — regex-based release scoring on top of quality profiles
- **Subtitle integration** — automatic search and download via SubDL and OpenSubtitles
- **Notifications** — Discord webhook and generic HTTP webhook on grab/import/failure events
- **WebDAV server** — built-in WebDAV for rclone to serve as the FUSE backing store

## 🐳 Docker

Public images are published to:

- `ghcr.io/drakkar-media/drakkar`

Build it yourself:

```bash
docker build -t drakkar .
```

## 🧑‍💻 Development

### Prerequisites

- Go 1.26+
- Node.js + npm
- Docker and Docker Compose

### Frontend

The web UI is a SvelteKit app that's embedded into the Go binary at build time.

```bash
cd web
npm install
npm run dev     # Vite dev server with hot reload
npm run check   # type-check
```

### Backend

```bash
make frontend   # build the SvelteKit app and embed it into internal/frontend/build
make build      # build the Go binary → bin/drakkar
make test       # go test ./...
```

### Full local stack

`docker-compose.dev.yml` mirrors the production compose file, but builds the `drakkar` image from your local source instead of pulling from GHCR — the rest of the stack (Postgres, Valkey, Seerr, NZBHydra2, rclone) is identical. Useful for testing changes end-to-end before releasing.

```bash
docker compose -f docker-compose.dev.yml up -d --build
```

> [!NOTE]
> Rebuild just the `drakkar` image after making changes with `docker compose -f docker-compose.dev.yml up -d --build drakkar`.

### Releasing

```bash
./release.sh v0.2.0
```

Bumps the version, commits, tags, and pushes. GitHub Actions then builds and publishes the image to `ghcr.io/drakkar-media/drakkar`.

## 🤝 Contributing

Contributions are welcome.

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/my-change`
3. Commit your changes and open a Pull Request

Please keep contributions focused and describe clearly what your change improves.

## 📄 License

MIT — see [LICENSE](LICENSE).

## ⚠️ Disclaimer

> [!WARNING]
> This project is provided as-is, without warranty of any kind. The author(s) are not responsible for any damage, data loss, misconfiguration, or security issues resulting from its use.
>
> Drakkar does not ship with movies, shows, subtitles, or indexer content. Do not use this project to infringe copyright, pirate media, or violate the laws or service terms that apply in your country or to the systems you connect it to.
