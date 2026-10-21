CREATE TABLE users (
    id                uuid PRIMARY KEY,
    username          text NOT NULL UNIQUE
                      CHECK (char_length(username) BETWEEN 3 AND 64 AND username ~ '^[a-z0-9_.-]+$'),
    password_hash     text NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    is_platform_admin boolean NOT NULL DEFAULT false,
    active            boolean NOT NULL DEFAULT true,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE groups (
    id         uuid PRIMARY KEY,
    name       text NOT NULL UNIQUE
               CHECK (name = btrim(name) AND char_length(name) BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
