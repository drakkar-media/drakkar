-- Expand movies, tv_shows, and episodes with richer metadata from TMDB/TVDB.
-- These columns improve release ranking, search query building, and UI display.

alter table movies
    add column if not exists overview             text,
    add column if not exists original_title       text,
    add column if not exists original_language    text,
    add column if not exists runtime_minutes      integer,
    add column if not exists poster_url           text,
    add column if not exists backdrop_url         text,
    add column if not exists popularity           numeric(10,3),
    add column if not exists vote_average         numeric(4,2),
    add column if not exists genres               text[],
    add column if not exists alternative_titles   text[];

alter table tv_shows
    add column if not exists overview             text,
    add column if not exists original_name        text,
    add column if not exists original_language    text,
    add column if not exists network              text,
    add column if not exists status               text,
    add column if not exists episode_run_time     integer,
    add column if not exists number_of_seasons    integer,
    add column if not exists number_of_episodes   integer,
    add column if not exists poster_url           text,
    add column if not exists backdrop_url         text,
    add column if not exists popularity           numeric(10,3),
    add column if not exists genres               text[],
    add column if not exists alternative_titles   text[];

alter table episodes
    add column if not exists overview             text,
    add column if not exists air_date             date,
    add column if not exists runtime_minutes      integer,
    add column if not exists still_url            text,
    add column if not exists vote_average         numeric(4,2),
    add column if not exists absolute_number      integer;
