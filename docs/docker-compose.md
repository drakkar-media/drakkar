# Docker Compose

Compose stack lives in repo root. App settings come from `./data/settings.json`;
process-level auth transport flags can come from `.env`.

Host mount rules:

- FUSE mount only at `/mnt/drakkar/vfs`
- media library paths stay outside FUSE
- `/mnt/drakkar` mount into container needs shared propagation

Auth transport environment:

- `DRAKKAR_AUTH_COOKIE_SECURE=true` always marks session cookies `Secure`.
- `DRAKKAR_AUTH_TRUST_PROXY_HEADERS=true` trusts `X-Forwarded-Proto`,
  `X-Forwarded-For`, and `X-Real-IP` from the reverse proxy for secure-cookie
  detection and login throttling. Enable only when clients cannot bypass that
  proxy.
