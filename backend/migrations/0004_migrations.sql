CREATE TABLE migration_drafts (
    id uuid PRIMARY KEY,
    database_id uuid NOT NULL REFERENCES database_connections (id),
    title text NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 200),
    sql text NOT NULL CHECK (octet_length(sql) BETWEEN 1 AND 262144),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision BETWEEN 1 AND 9007199254740991),
    expected_version bigint NOT NULL CHECK (expected_version BETWEEN 0 AND 9007199254740990),
    source_fingerprint text CHECK (source_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    created_by uuid NOT NULL REFERENCES users (id),
    updated_by uuid NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    applied_at timestamptz,
    UNIQUE (database_id, id)
);

CREATE INDEX migration_drafts_database_id_idx ON migration_drafts (database_id, updated_at DESC, id DESC);

CREATE TABLE migration_runs (
    id uuid PRIMARY KEY,
    database_id uuid NOT NULL REFERENCES database_connections (id),
    draft_id uuid NOT NULL,
    draft_revision bigint NOT NULL CHECK (draft_revision BETWEEN 1 AND 9007199254740991),
    idempotency_key uuid NOT NULL,
    input_hash bytea NOT NULL CHECK (octet_length(input_hash) = 32),
    sql_snapshot text NOT NULL CHECK (octet_length(sql_snapshot) BETWEEN 1 AND 262144),
    sql_hash bytea NOT NULL CHECK (octet_length(sql_hash) = 32),
    actor_id uuid NOT NULL REFERENCES users (id),
    actor_name_snapshot text NOT NULL,
    candidate_version bigint NOT NULL CHECK (candidate_version BETWEEN 1 AND 9007199254740991),
    version bigint,
    status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'outcome_unknown')),
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    error_code text,
    error_message text,
    FOREIGN KEY (database_id, draft_id) REFERENCES migration_drafts (database_id, id),
    UNIQUE (database_id, idempotency_key),
    CHECK (
        (status IN ('running', 'outcome_unknown') AND version IS NULL AND finished_at IS NULL)
        OR (status = 'failed' AND version IS NULL AND finished_at IS NOT NULL AND error_code IS NOT NULL AND error_message IS NOT NULL)
        OR (status = 'succeeded' AND version = candidate_version AND version IS NOT NULL AND finished_at IS NOT NULL AND error_code IS NULL AND error_message IS NULL)
    )
);

CREATE UNIQUE INDEX migration_runs_committed_version_idx ON migration_runs (database_id, version) WHERE status = 'succeeded';
CREATE UNIQUE INDEX migration_runs_unresolved_idx ON migration_runs (database_id) WHERE status IN ('running', 'outcome_unknown');
CREATE INDEX migration_runs_database_id_idx ON migration_runs (database_id, started_at DESC, id DESC);
CREATE INDEX migration_runs_status_idx ON migration_runs (status);

CREATE FUNCTION protect_migration_run() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.database_id, NEW.draft_id, NEW.draft_revision, NEW.idempotency_key,
        NEW.input_hash, NEW.sql_snapshot, NEW.sql_hash, NEW.actor_id, NEW.actor_name_snapshot,
        NEW.candidate_version, NEW.started_at)
        IS DISTINCT FROM
       (OLD.id, OLD.database_id, OLD.draft_id, OLD.draft_revision, OLD.idempotency_key,
        OLD.input_hash, OLD.sql_snapshot, OLD.sql_hash, OLD.actor_id, OLD.actor_name_snapshot,
        OLD.candidate_version, OLD.started_at) THEN
        RAISE EXCEPTION 'Migration run input is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.status IN ('succeeded', 'failed') AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'Migration run is terminal' USING ERRCODE = '23514';
    END IF;
    IF OLD.status = 'outcome_unknown' AND NEW.status = 'running' THEN
        RAISE EXCEPTION 'Migration run cannot restart' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER migration_runs_immutable BEFORE UPDATE ON migration_runs
FOR EACH ROW EXECUTE FUNCTION protect_migration_run();
