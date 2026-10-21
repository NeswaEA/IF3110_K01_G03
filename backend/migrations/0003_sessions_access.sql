CREATE TABLE sessions (
    id                  uuid PRIMARY KEY,
    user_id             uuid REFERENCES users (id),
    created_at          timestamptz NOT NULL DEFAULT now(),
    absolute_expires_at timestamptz NOT NULL,
    last_seen_at        timestamptz NOT NULL DEFAULT now(),
    rotated_at          timestamptz NOT NULL DEFAULT now(),
    generation          integer NOT NULL DEFAULT 1 CHECK (generation >= 1),
    rotation_required   boolean NOT NULL DEFAULT false,
    revoked_at          timestamptz,
    csrf_secret         bytea NOT NULL CHECK (octet_length(csrf_secret) = 32),
    CHECK (absolute_expires_at > created_at),
    CHECK (last_seen_at >= created_at AND last_seen_at <= absolute_expires_at),
    CHECK (rotated_at >= created_at AND rotated_at <= absolute_expires_at)
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);

CREATE FUNCTION prevent_session_deadline_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.absolute_expires_at IS DISTINCT FROM OLD.absolute_expires_at THEN
        RAISE EXCEPTION 'Session absolute deadline cannot be changed'
            USING ERRCODE = '23514', CONSTRAINT = 'sessions_absolute_expires_at_immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER sessions_absolute_expires_at_immutable
BEFORE UPDATE OF absolute_expires_at ON sessions
FOR EACH ROW EXECUTE FUNCTION prevent_session_deadline_change();

CREATE TABLE session_tokens (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    session_id uuid NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    generation integer NOT NULL CHECK (generation >= 1),
    is_current boolean NOT NULL,
    valid_until timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (valid_until >= created_at)
);

CREATE UNIQUE INDEX session_tokens_current_idx ON session_tokens (session_id) WHERE is_current;
CREATE INDEX session_tokens_session_id_idx ON session_tokens (session_id);

CREATE TABLE group_memberships (
    group_id uuid NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);

CREATE INDEX group_memberships_user_id_idx ON group_memberships (user_id, group_id);

CREATE TABLE group_database_access (
    group_id uuid NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    database_id uuid NOT NULL REFERENCES database_connections (id),
    level text NOT NULL CHECK (level IN ('view', 'edit')),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, database_id)
);

CREATE INDEX group_database_access_database_id_idx ON group_database_access (database_id, group_id);
