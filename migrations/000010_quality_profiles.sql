-- Quality profiles allow operators to configure release ranking preferences.
-- A profile defines resolution/source/codec/language weights that override
-- the built-in scoring constants in the ranking engine.
create table if not exists quality_profiles (
    id            bigserial primary key,
    name          text not null unique,
    is_default    boolean not null default false,
    resolutions   text[] not null default '{}',   -- ordered preference list e.g. {"2160p","1080p","720p"}
    sources       text[] not null default '{}',   -- e.g. {"BluRay","WEB-DL","WEBRip","HDTV"}
    codecs        text[] not null default '{}',   -- e.g. {"x265","x264","AV1"}
    languages     text[] not null default '{}',   -- e.g. {"nl","en"}
    min_size_mb   integer not null default 0,
    max_size_mb   integer not null default 0,     -- 0 = no limit
    created_at    timestamptz not null default now(),
    updated_at    timestamptz not null default now()
);

-- Seed a sensible default profile.
insert into quality_profiles (name, is_default, resolutions, sources, codecs, languages)
values (
    'Default',
    true,
    '{"1080p","2160p","720p"}',
    '{"WEB-DL","BluRay","WEBRip","HDTV"}',
    '{"x265","x264","AV1"}',
    '{"nl","en"}'
)
on conflict (name) do nothing;
