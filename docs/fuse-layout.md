# FUSE Layout

Required top-level virtual directories:

- `.ids`
- `completed-symlinks`
- `content`
- `nzbs`

Synthetic timestamp:

- `2000-01-01T00:00:00Z`

Operation matrix currently enforced in model tests. Live kernel mount still pending.

Implemented live behavior:

- `/nzbs` lists active NZB documents from PostgreSQL
- reading `/nzbs/<name>.nzb` returns original XML bytes
- unlink on `/nzbs/<name>.nzb` cancels matching queue item
- create/write/flush/release on `/nzbs` stage upload into `/mnt/drakkar/staging/nzbs` and import on flush/release
- `/completed-symlinks` lists persisted publication symlinks and resolves targets

Still pending:

- `/content` publication creation from indexing workflow
- reconstruction of published virtual media nodes after restart testing
