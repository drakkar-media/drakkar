-- Remove duplicate queue_items that accumulated when Seerr sync re-created
-- queue_items for library_items that already had one (with a different idempotency_key).
-- Keep the highest-priority row per library_item; on tie, keep the oldest (lowest id).
DELETE FROM queue_items
WHERE id IN (
    SELECT id FROM (
        SELECT id,
               ROW_NUMBER() OVER (
                   PARTITION BY library_item_id
                   ORDER BY
                       CASE state
                           WHEN 'available' THEN 1
                           WHEN 'selected'  THEN 2
                           WHEN 'requested' THEN
                               CASE WHEN selected_release_id IS NOT NULL THEN 3 ELSE 4 END
                           WHEN 'failed'    THEN 5
                           ELSE                  6
                       END ASC,
                       id ASC
               ) AS rn
        FROM queue_items
    ) ranked
    WHERE rn > 1
);

ALTER TABLE queue_items
    ADD CONSTRAINT queue_items_library_item_id_key UNIQUE (library_item_id);
