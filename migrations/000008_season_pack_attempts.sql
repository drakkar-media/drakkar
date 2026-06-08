-- Track season pack search attempts per TV show season so Drakkar can
-- prefer season packs but avoid hammering the indexers if none are available.
create table if not exists season_pack_attempts (
    id              bigserial primary key,
    tv_show_id      bigint not null references tv_shows(id) on delete cascade,
    season_number   integer not null,
    last_attempt_at timestamptz not null default now(),
    attempt_count   integer not null default 1,
    last_outcome    text not null default 'searching',
    unique (tv_show_id, season_number)
);

create index if not exists idx_season_pack_attempts_show_season
    on season_pack_attempts (tv_show_id, season_number);
