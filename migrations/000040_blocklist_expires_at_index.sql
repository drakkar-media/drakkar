-- Partial index on blocklist_items.expires_at for efficient TTL maintenance
-- and admin filtering without indexing permanent entries.
create index if not exists blocklist_items_expires_at_idx
    on blocklist_items (expires_at)
    where expires_at is not null;
