-- sessions and api_tokens were two near-identical tables (both: a hashed
-- bearer token tied to a user, with an optional expiry) backing two nearly
-- parallel sets of Go queries. Consolidated into one auth_tokens table with
-- a kind discriminator. This also fixes a real gap found while auditing the
-- two originals: PruneExpiredSessions existed but was never actually called
-- from anywhere, and api_tokens had no prune path at all -- expired rows of
-- both kinds simply accumulated forever. One prune query now covers both.
create table auth_tokens (
    id            bigserial primary key,
    user_id       bigint not null references users(id) on delete cascade,
    kind          text not null check (kind in ('session', 'api')),
    name          text,
    token_hash    text not null unique,
    created_at    timestamptz not null default now(),
    last_used_at  timestamptz,
    expires_at    timestamptz
);

create index auth_tokens_user_id_idx on auth_tokens (user_id, created_at desc);
create index auth_tokens_token_hash_idx on auth_tokens (token_hash);
create index auth_tokens_expires_at_idx on auth_tokens (expires_at) where expires_at is not null;

insert into auth_tokens (user_id, kind, token_hash, created_at, expires_at)
select user_id, 'session', token_hash, created_at, expires_at from sessions;

insert into auth_tokens (user_id, kind, name, token_hash, created_at, last_used_at, expires_at)
select user_id, 'api', name, token_hash, created_at, last_used_at, expires_at from api_tokens;

drop table sessions;
drop table api_tokens;
