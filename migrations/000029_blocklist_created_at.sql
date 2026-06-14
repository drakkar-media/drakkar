-- Add created_at to blocklist_items for display and filtering.
ALTER TABLE blocklist_items
    ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now();

-- Backfill existing rows with a placeholder timestamp so the column is non-null.
UPDATE blocklist_items SET created_at = now() WHERE created_at IS NULL;
