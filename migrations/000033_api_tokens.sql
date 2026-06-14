CREATE TABLE api_tokens (
    id            bigserial PRIMARY KEY,
    user_id       bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          text NOT NULL,
    token_hash    text NOT NULL UNIQUE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz,
    expires_at    timestamptz
);

CREATE INDEX api_tokens_user_id_idx ON api_tokens (user_id, created_at DESC);
CREATE INDEX api_tokens_token_hash_idx ON api_tokens (token_hash);
