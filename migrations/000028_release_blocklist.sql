-- Release block rules: persistent blocklist for LQ groups, title patterns, and regexes.
-- Inspired by TRaSH Guides LQ / LQ Release Title custom formats for Radarr/Sonarr.
-- Rules can block a release outright or apply a score penalty.

CREATE TABLE IF NOT EXISTS release_block_rules (
    id            bigserial   PRIMARY KEY,
    rule_type     text        NOT NULL CHECK (rule_type IN ('release_group','title_pattern','regex','missing_release_group')),
    pattern       text        NOT NULL DEFAULT '',
    media_type    text        NOT NULL DEFAULT 'both' CHECK (media_type IN ('movie','tv','both')),
    action        text        NOT NULL DEFAULT 'block' CHECK (action IN ('block','penalty')),
    score_penalty integer     NOT NULL DEFAULT 0,
    enabled       boolean     NOT NULL DEFAULT true,
    source        text        NOT NULL DEFAULT 'custom' CHECK (source IN ('default','trash','custom')),
    note          text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_release_block_rules_enabled  ON release_block_rules (enabled);
CREATE INDEX IF NOT EXISTS idx_release_block_rules_type     ON release_block_rules (rule_type);

-- ── Default blocked release groups (TRaSH Guides LQ-inspired) ─────────────────
INSERT INTO release_block_rules (rule_type, pattern, media_type, action, source, note) VALUES
  ('release_group','24xHD',          'both','block','default','TRaSH LQ'),
  ('release_group','41RGB',          'both','block','default','TRaSH LQ'),
  ('release_group','4K4U',           'both','block','default','TRaSH LQ'),
  ('release_group','AOC',            'both','block','default','TRaSH LQ'),
  ('release_group','AROMA',          'both','block','default','TRaSH LQ'),
  ('release_group','aXXo',           'both','block','default','TRaSH LQ'),
  ('release_group','AZAZE',          'both','block','default','TRaSH LQ'),
  ('release_group','BARC0DE',        'both','block','default','TRaSH LQ'),
  ('release_group','BAUCKLEY',       'both','block','default','TRaSH LQ'),
  ('release_group','BdC',            'both','block','default','TRaSH LQ'),
  ('release_group','beAst',          'both','block','default','TRaSH LQ'),
  ('release_group','BTM',            'both','block','default','TRaSH LQ'),
  ('release_group','C1NEM4',         'both','block','default','TRaSH LQ'),
  ('release_group','C4K',            'both','block','default','TRaSH LQ'),
  ('release_group','CDDHD',          'both','block','default','TRaSH LQ'),
  ('release_group','CHAOS',          'both','block','default','TRaSH LQ'),
  ('release_group','CHD',            'both','block','default','TRaSH LQ'),
  ('release_group','CiNE',           'both','block','default','TRaSH LQ'),
  ('release_group','COLLECTiVE',     'both','block','default','TRaSH LQ'),
  ('release_group','CREATiVE24',     'both','block','default','TRaSH LQ'),
  ('release_group','CrEwSaDe',       'both','block','default','TRaSH LQ'),
  ('release_group','CTFOH',          'both','block','default','TRaSH LQ'),
  ('release_group','d3g',            'both','block','default','TRaSH LQ'),
  ('release_group','DDR',            'both','block','default','TRaSH LQ'),
  ('release_group','DNL',            'both','block','default','TRaSH LQ'),
  ('release_group','DRX',            'both','block','default','TRaSH LQ'),
  ('release_group','E',              'both','block','default','TRaSH LQ'),
  ('release_group','EPiC',           'both','block','default','TRaSH LQ'),
  ('release_group','EuReKA',         'both','block','default','TRaSH LQ'),
  ('release_group','FaNGDiNG0',      'both','block','default','TRaSH LQ'),
  ('release_group','Feranki1980',    'both','block','default','TRaSH LQ'),
  ('release_group','FGT',            'both','block','default','TRaSH LQ'),
  ('release_group','FMD',            'both','block','default','TRaSH LQ'),
  ('release_group','FRDS',           'both','block','default','TRaSH LQ'),
  ('release_group','FZHD',           'both','block','default','TRaSH LQ'),
  ('release_group','GalaxyRG',       'both','block','default','TRaSH LQ'),
  ('release_group','GHD',            'both','block','default','TRaSH LQ'),
  ('release_group','GPTHD',          'both','block','default','TRaSH LQ'),
  ('release_group','HDHUB4U',        'both','block','default','TRaSH LQ'),
  ('release_group','HDS',            'both','block','default','TRaSH LQ'),
  ('release_group','HDT',            'both','block','default','TRaSH LQ'),
  ('release_group','HDTime',         'both','block','default','TRaSH LQ'),
  ('release_group','HDWinG',         'both','block','default','TRaSH LQ'),
  ('release_group','iNTENSO',        'both','block','default','TRaSH LQ'),
  ('release_group','iPlanet',        'both','block','default','TRaSH LQ'),
  ('release_group','iVy',            'both','block','default','TRaSH LQ'),
  ('release_group','jennaortega',    'both','block','default','TRaSH LQ'),
  ('release_group','jennaortegaUHD', 'both','block','default','TRaSH LQ'),
  ('release_group','JFF',            'both','block','default','TRaSH LQ'),
  ('release_group','KC',             'both','block','default','TRaSH LQ'),
  ('release_group','KiNGDOM',        'both','block','default','TRaSH LQ'),
  ('release_group','KIRA',           'both','block','default','TRaSH LQ'),
  ('release_group','L0SERNIGHT',     'both','block','default','TRaSH LQ'),
  ('release_group','V3SP4EV3R',      'both','block','default','TRaSH LQ + local custom')
ON CONFLICT DO NOTHING;

-- ── Default blocked title patterns (TRaSH LQ Release Title inspired) ──────────
INSERT INTO release_block_rules (rule_type, pattern, media_type, action, source, note) VALUES
  ('title_pattern','zipx',         'both','block','default','Zipped executable in release'),
  ('title_pattern','.scr',         'both','block','default','Screensaver — potentially malicious'),
  ('title_pattern','.lnk',         'both','block','default','Windows shortcut — potentially malicious'),
  ('title_pattern','BR-DISK',      'both','block','default','Unencoded Blu-ray disc'),
  ('title_pattern','BRDISK',       'both','block','default','Unencoded Blu-ray disc'),
  ('title_pattern','BD3D',         'both','block','default','3D Blu-ray'),
  ('title_pattern','BluRay 3D',    'both','block','default','3D Blu-ray'),
  ('title_pattern','BluRay-3D',    'both','block','default','3D Blu-ray'),
  ('title_pattern','AI Upscale',   'both','block','default','AI upscaled — not real quality'),
  ('title_pattern','AI-Upscale',   'both','block','default','AI upscaled — not real quality'),
  ('title_pattern','AIUS',         'both','block','default','AI upscaled short tag'),
  ('title_pattern','Upscaled',     'both','block','default','AI upscaled'),
  ('title_pattern','The Upscaler', 'both','block','default','AI upscaled group/tag'),
  ('title_pattern','Regrade',      'both','block','default','Colour-regraded release'),
  ('title_pattern','Sing Along',   'both','block','default','Sing-along version'),
  ('title_pattern','Sing-Along',   'both','block','default','Sing-along version')
ON CONFLICT DO NOTHING;
