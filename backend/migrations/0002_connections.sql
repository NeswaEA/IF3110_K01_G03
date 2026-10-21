-- Registered target databases.
-- config_encrypted is AES-GCM nonce||ciphertext; the key lives only in the environment.
CREATE TABLE database_connections (
    id               uuid PRIMARY KEY,
    name             text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    driver           text        NOT NULL CHECK (driver IN ('postgres', 'sqlite')),
    config_encrypted bytea       NOT NULL,
    revision         bigint      NOT NULL DEFAULT 1 CHECK (revision >= 1 AND revision <= 9007199254740991),
    active           boolean     NOT NULL DEFAULT true,
    migration_ready  boolean     NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    last_checked_at  timestamptz,
    last_error_code  text
);

CREATE INDEX database_connections_name_idx ON database_connections (name, id);
