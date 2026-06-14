-- Add source tracking to custom_formats so TRaSH-imported formats can be
-- distinguished from user-created ones and from Drakkar's built-in defaults.
ALTER TABLE custom_formats
    ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'custom'
        CHECK (source IN ('default','trash','custom'));

-- Unique name index used by the bulk-import ON CONFLICT (name) upsert.
CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_formats_name ON custom_formats (name);
