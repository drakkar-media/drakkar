CREATE TABLE users (
    id            bigserial PRIMARY KEY,
    username      text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    role          text NOT NULL DEFAULT 'admin',
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id          bigserial PRIMARY KEY,
    user_id     bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  text NOT NULL UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL
);

CREATE INDEX sessions_token_hash_idx ON sessions (token_hash);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);
