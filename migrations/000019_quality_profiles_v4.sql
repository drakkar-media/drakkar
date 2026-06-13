-- Replace all old seeded quality profiles with Radarr/Sonarr-matching presets.
-- User-created profiles (not in our known seed list) are preserved.

DELETE FROM quality_profiles WHERE name IN (
    'Default', 'Movie HD', 'Movie Remux 4K', 'TV Standard', '4K HDR',
    'HD-1080p', 'HD-720p', 'HD-720p/1080p', 'HD/UHD Auto', 'Ultra-HD', 'SD', 'Any'
);

-- Insert canonical Radarr/Sonarr-style profiles
INSERT INTO quality_profiles (name, is_default, resolutions, sources, codecs, languages, audio_formats, hdr_formats, exclude_patterns, prefer_proper, prefer_repack, reject_cam, min_size_mb, max_size_mb)
VALUES
-- HD - 720p/1080p  (most common default — 720p + 1080p WEB/Bluray)
('HD - 720p/1080p', false,
 ARRAY['1080p','720p'],
 ARRAY['WEB-DL','BluRay','WEBRip','Remux','HDTV'],
 ARRAY['x265','x264'],
 ARRAY['nl','en'],
 ARRAY['TrueHD','DTS-HD','DTS','DD+','AC3','AAC'],
 ARRAY['HDR10','SDR'],
 ARRAY[]::text[], true, true, true, 0, 0),

-- HD-720p  (720p only)
('HD-720p', false,
 ARRAY['720p'],
 ARRAY['WEB-DL','BluRay','WEBRip','HDTV'],
 ARRAY['x265','x264'],
 ARRAY['nl','en'],
 ARRAY['DTS','DD+','AC3','AAC'],
 ARRAY['SDR'],
 ARRAY[]::text[], true, true, true, 0, 0),

-- HD-1080p  (1080p only, including Remux)
('HD-1080p', false,
 ARRAY['1080p'],
 ARRAY['WEB-DL','BluRay','WEBRip','Remux','HDTV'],
 ARRAY['x265','x264'],
 ARRAY['nl','en'],
 ARRAY['TrueHD','DTS-HD','DTS','DD+','AC3','AAC'],
 ARRAY['HDR10','SDR'],
 ARRAY[]::text[], true, true, true, 0, 0),

-- HD/UHD Auto  (720p through 4K, no CAM/DVD)
('HD/UHD Auto', false,
 ARRAY['2160p','1080p','720p'],
 ARRAY['WEB-DL','BluRay','WEBRip','Remux','HDTV'],
 ARRAY['x265','HEVC','x264'],
 ARRAY['nl','en'],
 ARRAY['TrueHD','DTS-HD','DTS','DD+','AC3','AAC'],
 ARRAY['HDR10','DV','SDR'],
 ARRAY[]::text[], true, true, true, 0, 0),

-- Ultra-HD  (4K only)
('Ultra-HD', false,
 ARRAY['2160p'],
 ARRAY['WEB-DL','BluRay','WEBRip','Remux','HDTV'],
 ARRAY['x265','HEVC','AV1'],
 ARRAY['nl','en'],
 ARRAY['TrueHD','DTS-HD','DTS','DD+','AC3','AAC'],
 ARRAY['HDR10','DV','HDR10+'],
 ARRAY[]::text[], true, true, true, 0, 0),

-- SD  (480p/576p + low-quality sources)
('SD', false,
 ARRAY['576p','480p'],
 ARRAY['WEB-DL','BluRay','WEBRip','HDTV','DVDRip'],
 ARRAY['x265','x264'],
 ARRAY['nl','en'],
 ARRAY['DD+','AC3','AAC'],
 ARRAY['SDR'],
 ARRAY[]::text[], true, true, true, 0, 0),

-- Any  (all qualities — permissive fallback)
('Any', false,
 ARRAY['2160p','1080p','720p','576p','480p'],
 ARRAY['WEB-DL','BluRay','WEBRip','Remux','HDTV','DVDRip'],
 ARRAY['x265','HEVC','x264','AVC','AV1'],
 ARRAY['nl','en'],
 ARRAY['TrueHD','DTS-HD','DTS','DD+','AC3','AAC'],
 ARRAY['HDR10','DV','HDR10+','SDR'],
 ARRAY[]::text[], true, true, false, 0, 0)
ON CONFLICT (name) DO UPDATE SET
    resolutions      = excluded.resolutions,
    sources          = excluded.sources,
    codecs           = excluded.codecs,
    audio_formats    = excluded.audio_formats,
    hdr_formats      = excluded.hdr_formats,
    prefer_proper    = excluded.prefer_proper,
    prefer_repack    = excluded.prefer_repack,
    reject_cam       = excluded.reject_cam;

-- Add per-library-item quality profile override
ALTER TABLE library_items
    ADD COLUMN IF NOT EXISTS quality_profile_id bigint REFERENCES quality_profiles(id) ON DELETE SET NULL;
