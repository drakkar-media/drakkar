-- Blocklist entries for a "confirmed gone article" reason (early preflight,
-- preflight, strict health, article health check) previously discarded the
-- specific NNTP message-id that failed, so a wrong verdict (e.g. a
-- throttle-induced false 430 during a connection-instability incident) could
-- never be re-checked or corrected later -- it was blocklisted forever on a
-- single unverifiable snapshot. message_id is nullable: manual rejects and
-- structural/archive rejects have no single message-id to record.
alter table blocklist_items add column if not exists message_id text;
