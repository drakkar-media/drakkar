-- Add exclude patterns to quality profiles
ALTER TABLE quality_profiles
    ADD COLUMN IF NOT EXISTS exclude_patterns text[] NOT NULL DEFAULT '{}';

-- Quality definitions: per-quality-tier size limits, separate for movies and TV
CREATE TABLE IF NOT EXISTS quality_definitions (
    id          bigserial PRIMARY KEY,
    media_type  text    NOT NULL,  -- 'movie' or 'episode'
    quality_key text    NOT NULL,  -- machine key, e.g. 'webdl1080p'
    title       text    NOT NULL,  -- display name, e.g. 'WEBDL-1080p'
    min_size_mb integer NOT NULL DEFAULT 0,
    max_size_mb integer NOT NULL DEFAULT 0,  -- 0 = unlimited
    sort_order  integer NOT NULL DEFAULT 0,
    UNIQUE (media_type, quality_key)
);

-- Movie quality definitions (Radarr-style)
INSERT INTO quality_definitions (media_type, quality_key, title, min_size_mb, max_size_mb, sort_order) VALUES
('movie', 'unknown',    'Unknown',      0,  100,  0),
('movie', 'workprint',  'WORKPRINT',    0,  100,  1),
('movie', 'cam',        'CAM',          0,  100,  2),
('movie', 'telesync',   'TELESYNC',     0,  100,  3),
('movie', 'telecine',   'TELECINE',     0,  100,  4),
('movie', 'regional',   'REGIONAL',     0,  100,  5),
('movie', 'dvdscr',     'DVDSCR',       0,  100,  6),
('movie', 'sdtv',       'SDTV',         0,  100,  7),
('movie', 'dvd',        'DVD',          0,  100,  8),
('movie', 'dvdr',       'DVD-R',        0,  100,  9),
('movie', 'webdl480p',  'WEBDL-480p',   0,  100, 10),
('movie', 'webrip480p', 'WEBRip-480p',  0,  100, 11),
('movie', 'bluray480p', 'Bluray-480p',  0,  100, 12),
('movie', 'bluray576p', 'Bluray-576p',  0,  100, 13),
('movie', 'hdtv720p',   'HDTV-720p',    0,  100, 14),
('movie', 'webdl720p',  'WEBDL-720p',   0,  100, 15),
('movie', 'webrip720p', 'WEBRip-720p',  0,  100, 16),
('movie', 'bluray720p', 'Bluray-720p',  0,  100, 17),
('movie', 'hdtv1080p',  'HDTV-1080p',   0,  100, 18),
('movie', 'webdl1080p', 'WEBDL-1080p',  0,  130, 19),
('movie', 'webrip1080p','WEBRip-1080p', 0,  130, 20),
('movie', 'bluray1080p','Bluray-1080p', 0,  155, 21),
('movie', 'remux1080p', 'Remux-1080p',  0,    0, 22),
('movie', 'hdtv2160p',  'HDTV-2160p',   0,    0, 23),
('movie', 'webdl2160p', 'WEBDL-2160p',  0,    0, 24),
('movie', 'webrip2160p','WEBRip-2160p', 0,    0, 25),
('movie', 'bluray2160p','Bluray-2160p', 0,    0, 26),
('movie', 'remux2160p', 'Remux-2160p',  0,    0, 27),
('movie', 'brdisk',     'BR-DISK',       0,    0, 28),
('movie', 'rawhd',      'Raw-HD',        0,    0, 29)
ON CONFLICT (media_type, quality_key) DO NOTHING;

-- TV quality definitions (Sonarr-style)
INSERT INTO quality_definitions (media_type, quality_key, title, min_size_mb, max_size_mb, sort_order) VALUES
('episode', 'unknown',    'Unknown',           1,  200,  0),
('episode', 'sdtv',       'SDTV',              2,  100,  1),
('episode', 'webrip480p', 'WEBRip-480p',       2,  100,  2),
('episode', 'webdl480p',  'WEBDL-480p',        2,  100,  3),
('episode', 'dvd',        'DVD',               2,  100,  4),
('episode', 'bluray480p', 'Bluray-480p',       2,  100,  5),
('episode', 'bluray576p', 'Bluray-576p',       2,  100,  6),
('episode', 'hdtv720p',   'HDTV-720p',         3,  125,  7),
('episode', 'hdtv1080p',  'HDTV-1080p',        4,  125,  8),
('episode', 'rawhd',      'Raw-HD',            4, 1000,  9),
('episode', 'webrip720p', 'WEBRip-720p',       3,  130, 10),
('episode', 'webdl720p',  'WEBDL-720p',        3,  130, 11),
('episode', 'bluray720p', 'Bluray-720p',       4,  130, 12),
('episode', 'webrip1080p','WEBRip-1080p',      4,  130, 13),
('episode', 'webdl1080p', 'WEBDL-1080p',       4,  130, 14),
('episode', 'bluray1080p','Bluray-1080p',      4,  155, 15),
('episode', 'remux1080p', 'Bluray-1080p Remux',35, 1000, 16),
('episode', 'hdtv2160p',  'HDTV-2160p',       35,  200, 17),
('episode', 'webrip2160p','WEBRip-2160p',     35, 1000, 18),
('episode', 'webdl2160p', 'WEBDL-2160p',      35, 1000, 19),
('episode', 'bluray2160p','Bluray-2160p',     35, 1000, 20),
('episode', 'remux2160p', 'Bluray-2160p Remux',35,1000, 21)
ON CONFLICT (media_type, quality_key) DO NOTHING;

-- Add Sonarr/Radarr-style preset profiles
INSERT INTO quality_profiles
    (name, is_default, resolutions, sources, codecs, languages, audio_formats, hdr_formats,
     prefer_proper, prefer_repack, reject_cam, min_size_mb, max_size_mb)
VALUES
('HD-1080p', false,
 ARRAY['1080p'], ARRAY['WEB-DL','WEBRip','BluRay'], ARRAY['x265','x264'],
 ARRAY['nl','en'], ARRAY['DD+','AC3','AAC'], ARRAY['SDR'],
 true, true, true, 0, 0),
('HD-720p', false,
 ARRAY['720p'], ARRAY['WEB-DL','WEBRip','BluRay'], ARRAY['x265','x264'],
 ARRAY['nl','en'], ARRAY['DD+','AC3','AAC'], ARRAY['SDR'],
 true, true, true, 0, 0),
('HD-720p/1080p', false,
 ARRAY['1080p','720p'], ARRAY['WEB-DL','WEBRip','BluRay'], ARRAY['x265','x264'],
 ARRAY['nl','en'], ARRAY['DD+','AC3','AAC'], ARRAY['SDR'],
 true, true, true, 0, 0),
('HD/UHD Auto', false,
 ARRAY['2160p','1080p','720p'], ARRAY['WEB-DL','WEBRip','BluRay','Remux'], ARRAY['x265','x264','AV1'],
 ARRAY['nl','en'], ARRAY['Atmos','TrueHD','DD+','AC3'], ARRAY['DV','HDR10+','HDR10','SDR'],
 true, true, true, 0, 0),
('Ultra-HD', false,
 ARRAY['2160p'], ARRAY['WEB-DL','WEBRip','BluRay','Remux'], ARRAY['x265','x264','AV1'],
 ARRAY['nl','en'], ARRAY['Atmos','TrueHD','DTS-HD','DD+'], ARRAY['DV','HDR10+','HDR10','SDR'],
 true, true, true, 0, 0),
('SD', false,
 ARRAY['480p','576p','720p'], ARRAY['WEB-DL','WEBRip','HDTV'], ARRAY['x264'],
 ARRAY['nl','en'], ARRAY['AC3','AAC'], ARRAY['SDR'],
 true, false, true, 0, 0)
ON CONFLICT (name) DO NOTHING;
