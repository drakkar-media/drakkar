-- Extend quality_profiles with audio formats, HDR formats and ranking flags.
-- Adds RTN-inspired per-attribute scoring capabilities.
alter table quality_profiles
    add column if not exists audio_formats  text[] not null default '{}',
    add column if not exists hdr_formats    text[] not null default '{}',
    add column if not exists prefer_proper  boolean not null default true,
    add column if not exists prefer_repack  boolean not null default true,
    add column if not exists reject_cam     boolean not null default true;

-- Update the existing Default profile with sensible audio/HDR defaults.
update quality_profiles
set    audio_formats = '{"TrueHD","DTS-HD","DTS","DD+","AC3","AAC"}',
       hdr_formats   = '{"HDR10","DV","HDR10+","HLG","SDR"}',
       prefer_proper = true,
       prefer_repack  = true,
       reject_cam    = true
where  name = 'Default';

-- Seed additional preset profiles.
insert into quality_profiles
    (name, is_default, resolutions, sources, codecs, languages, audio_formats, hdr_formats,
     min_size_mb, max_size_mb, prefer_proper, prefer_repack, reject_cam)
values
    (
        'Movie HD',
        false,
        '{"1080p","720p"}',
        '{"WEB-DL","BluRay","WEBRip","HDTV"}',
        '{"x265","x264"}',
        '{"nl","en"}',
        '{"TrueHD","DTS-HD","DTS","DD+","AC3","AAC"}',
        '{"HDR10","SDR"}',
        1000, 25000, true, true, true
    ),
    (
        'Movie Remux 4K',
        false,
        '{"2160p","1080p"}',
        '{"BluRay","Remux","WEB-DL"}',
        '{"x265","AV1","x264"}',
        '{"nl","en"}',
        '{"TrueHD","DTS-HD","Atmos","DTS"}',
        '{"DV","HDR10+","HDR10","HLG"}',
        15000, 0, true, true, true
    ),
    (
        'TV Standard',
        false,
        '{"1080p","720p"}',
        '{"WEB-DL","WEBRip","HDTV"}',
        '{"x265","x264"}',
        '{"nl","en"}',
        '{"DD+","AC3","DTS","AAC"}',
        '{"HDR10","SDR"}',
        200, 8000, true, true, true
    ),
    (
        '4K HDR',
        false,
        '{"2160p"}',
        '{"WEB-DL","BluRay","Remux"}',
        '{"x265","AV1"}',
        '{"nl","en"}',
        '{"TrueHD","DTS-HD","Atmos","DD+"}',
        '{"DV","HDR10+","HDR10"}',
        5000, 0, true, true, true
    )
on conflict (name) do nothing;
