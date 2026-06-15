-- Partial index on blocklist_items.expires_at for efficient TTL-filtered lookups.
-- loadBlocklistMap filters with "WHERE expires_at IS NULL OR expires_at > now()";
-- this index covers the "expires_at > now()" branch without indexing permanent items.
create index if not exists blocklist_items_expires_at_idx
    on blocklist_items (expires_at)
    where expires_at is not null;
