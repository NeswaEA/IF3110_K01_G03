-- The synchronous draft/run model is replaced by migrations, immutable revisions,
-- change requests and queued executions.
DROP TABLE migration_runs, migration_drafts;
DROP FUNCTION protect_migration_run();

CREATE FUNCTION reject_append_only_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = '23514';
END;
$$;

CREATE TABLE migrations (
    id uuid PRIMARY KEY,
    database_id uuid NOT NULL REFERENCES database_connections (id),
    title text NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
    current_revision bigint NOT NULL DEFAULT 1 CHECK (current_revision BETWEEN 1 AND 9007199254740991),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'applied', 'rolled_back', 'superseded', 'partial')),
    version bigint CHECK (version BETWEEN 1 AND 9007199254740991),
    locked_revision bigint,
    locked_at timestamptz,
    source_fingerprint text CHECK (source_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    created_by uuid NOT NULL REFERENCES users (id),
    updated_by uuid NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (database_id, id),
    CHECK ((locked_at IS NULL) = (locked_revision IS NULL)),
    CHECK (status = 'pending' OR locked_at IS NOT NULL),
    CHECK (locked_revision IS NULL OR current_revision = locked_revision)
);

CREATE UNIQUE INDEX migrations_version_idx ON migrations (database_id, version)
    WHERE status IN ('applied', 'rolled_back', 'partial') AND version IS NOT NULL;
CREATE INDEX migrations_database_id_idx ON migrations (database_id, updated_at DESC, id DESC);

CREATE FUNCTION protect_migration() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.database_id, NEW.created_by, NEW.created_at)
        IS DISTINCT FROM (OLD.id, OLD.database_id, OLD.created_by, OLD.created_at) THEN
        RAISE EXCEPTION 'Migration identity is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.locked_at IS NOT NULL AND (NEW.locked_at, NEW.locked_revision)
        IS DISTINCT FROM (OLD.locked_at, OLD.locked_revision) THEN
        RAISE EXCEPTION 'Migration lock is permanent' USING ERRCODE = '23514';
    END IF;
    IF NEW.current_revision < OLD.current_revision THEN
        RAISE EXCEPTION 'Migration revision cannot go back' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER migrations_protected BEFORE UPDATE ON migrations
FOR EACH ROW EXECUTE FUNCTION protect_migration();

-- {"schema","table","key_column": string, "batch_size": integer >= 1, "pause_ms": integer 0..60000}, no other keys.
-- STRICT: a NULL config (normal mode) yields NULL, which a CHECK accepts.
CREATE FUNCTION valid_batch_config(b jsonb) RETURNS boolean LANGUAGE plpgsql IMMUTABLE STRICT AS $$
BEGIN
    IF jsonb_typeof(b) <> 'object'
        OR b - ARRAY['schema', 'table', 'key_column', 'batch_size', 'pause_ms'] <> '{}'::jsonb
        OR jsonb_typeof(b -> 'schema') IS DISTINCT FROM 'string'
        OR jsonb_typeof(b -> 'table') IS DISTINCT FROM 'string'
        OR jsonb_typeof(b -> 'key_column') IS DISTINCT FROM 'string'
        OR jsonb_typeof(b -> 'batch_size') IS DISTINCT FROM 'number'
        OR jsonb_typeof(b -> 'pause_ms') IS DISTINCT FROM 'number' THEN
        RETURN false;
    END IF;
    RETURN b ->> 'schema' <> '' AND b ->> 'table' <> '' AND b ->> 'key_column' <> ''
        AND (b ->> 'batch_size')::numeric = trunc((b ->> 'batch_size')::numeric)
        AND (b ->> 'batch_size')::numeric BETWEEN 1 AND 2147483647
        AND (b ->> 'pause_ms')::numeric = trunc((b ->> 'pause_ms')::numeric)
        AND (b ->> 'pause_ms')::numeric BETWEEN 0 AND 60000;
END;
$$;

CREATE TABLE migration_revisions (
    migration_id uuid NOT NULL REFERENCES migrations (id),
    revision bigint NOT NULL CHECK (revision BETWEEN 1 AND 9007199254740991),
    up_sql text NOT NULL CHECK (octet_length(up_sql) BETWEEN 1 AND 262144),
    down_sql text CHECK (octet_length(down_sql) BETWEEN 1 AND 262144),
    irreversible_reason text CHECK (char_length(irreversible_reason) BETWEEN 1 AND 500),
    mode text NOT NULL CHECK (mode IN ('normal', 'batch')),
    batch jsonb CHECK (valid_batch_config(batch)),
    content_hash bytea NOT NULL CHECK (octet_length(content_hash) = 32),
    lint jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(lint) = 'array'),
    created_by uuid NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (migration_id, revision),
    CHECK ((down_sql IS NULL) <> (irreversible_reason IS NULL)),
    CHECK ((mode = 'batch') = (batch IS NOT NULL))
);

CREATE TRIGGER migration_revisions_append_only BEFORE UPDATE OR DELETE ON migration_revisions
FOR EACH ROW EXECUTE FUNCTION reject_append_only_change();

CREATE TABLE change_requests (
    id uuid PRIMARY KEY,
    database_id uuid NOT NULL,
    migration_id uuid NOT NULL,
    action text NOT NULL CHECK (action IN ('apply', 'rollback', 'forward')),
    migration_revision bigint NOT NULL,
    content_hash bytea NOT NULL CHECK (octet_length(content_hash) = 32),
    head_at_request bigint NOT NULL CHECK (head_at_request BETWEEN 0 AND 9007199254740991),
    target_execution_id uuid,
    batch_size integer CHECK (batch_size >= 1),
    note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 2000),
    status text NOT NULL DEFAULT 'pending_review'
        CHECK (status IN ('pending_review', 'approved', 'rejected', 'cancelled', 'executed')),
    requested_by uuid NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    closed_at timestamptz,
    UNIQUE (database_id, id),
    FOREIGN KEY (database_id, migration_id) REFERENCES migrations (database_id, id),
    FOREIGN KEY (migration_id, migration_revision) REFERENCES migration_revisions (migration_id, revision),
    CHECK (target_execution_id IS NULL OR action = 'rollback'),
    CHECK ((status IN ('rejected', 'cancelled', 'executed')) = (closed_at IS NOT NULL))
);

CREATE UNIQUE INDEX change_requests_open_idx ON change_requests (migration_id)
    WHERE status IN ('pending_review', 'approved');
CREATE INDEX change_requests_database_id_idx ON change_requests (database_id, status, updated_at DESC);

CREATE TABLE migration_executions (
    id uuid PRIMARY KEY,
    database_id uuid NOT NULL,
    number bigint NOT NULL CHECK (number BETWEEN 1 AND 9007199254740991),
    migration_id uuid NOT NULL,
    change_request_id uuid UNIQUE,
    action text NOT NULL CHECK (action IN ('apply', 'rollback', 'forward', 'resume', 'dry_run')),
    direction text NOT NULL CHECK (direction IN ('up', 'down')),
    mode text NOT NULL CHECK (mode IN ('normal', 'batch')),
    migration_revision bigint NOT NULL,
    content_hash bytea NOT NULL CHECK (octet_length(content_hash) = 32),
    status text NOT NULL CHECK (status IN ('queued', 'held', 'running', 'succeeded', 'failed',
        'partial', 'partially_reverted', 'outcome_unknown', 'cancelled')),
    attempt integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL CHECK (max_attempts >= 1),
    next_attempt_at timestamptz,
    version bigint CHECK (version BETWEEN 1 AND 9007199254740991),
    reverts_execution_id uuid,
    resumes_execution_id uuid,
    chain_root_id uuid NOT NULL,
    batch_size integer CHECK (batch_size >= 1),
    scope_from bigint,
    scope_to bigint,
    cursor bigint,
    batches_committed integer NOT NULL DEFAULT 0 CHECK (batches_committed >= 0),
    rows_committed bigint NOT NULL DEFAULT 0 CHECK (rows_committed >= 0),
    held_reason text,
    stop_requested_at timestamptz,
    reconcile_requested boolean NOT NULL DEFAULT false,
    worker_id text,
    heartbeat_at timestamptz,
    lock_requested_at timestamptz,
    enqueued_by uuid NOT NULL REFERENCES users (id),
    enqueued_by_name text NOT NULL,
    enqueued_at timestamptz NOT NULL,
    started_at timestamptz,
    finished_at timestamptz,
    error_code text,
    error_message text,
    trace_id text,
    history_seq bigint,
    result jsonb,
    UNIQUE (database_id, id),
    UNIQUE (database_id, number),
    FOREIGN KEY (database_id, migration_id) REFERENCES migrations (database_id, id),
    FOREIGN KEY (migration_id, migration_revision) REFERENCES migration_revisions (migration_id, revision),
    FOREIGN KEY (database_id, change_request_id) REFERENCES change_requests (database_id, id),
    FOREIGN KEY (database_id, reverts_execution_id) REFERENCES migration_executions (database_id, id),
    FOREIGN KEY (database_id, resumes_execution_id) REFERENCES migration_executions (database_id, id),
    FOREIGN KEY (database_id, chain_root_id) REFERENCES migration_executions (database_id, id),
    -- resume continues the approval of its chain root; dry-run needs none
    CHECK ((change_request_id IS NULL) = (action IN ('dry_run', 'resume'))),
    CHECK ((action = 'resume') = (resumes_execution_id IS NOT NULL)),
    CHECK (action = 'resume' OR chain_root_id = id),
    CHECK (action = 'resume' OR (direction = 'down') = (action = 'rollback')),
    CHECK (attempt BETWEEN 0 AND max_attempts),
    CHECK (result IS NULL OR action = 'dry_run')
);

-- Second guard behind the target lock: one running execution per database.
CREATE UNIQUE INDEX migration_executions_running_idx ON migration_executions (database_id) WHERE status = 'running';
CREATE INDEX migration_executions_status_idx ON migration_executions (database_id, status);
CREATE INDEX migration_executions_heartbeat_idx ON migration_executions (status, heartbeat_at);
CREATE INDEX migration_executions_chain_idx ON migration_executions (chain_root_id);

ALTER TABLE change_requests ADD FOREIGN KEY (database_id, target_execution_id)
    REFERENCES migration_executions (database_id, id);

CREATE FUNCTION protect_migration_execution() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.database_id, NEW.number, NEW.migration_id, NEW.change_request_id, NEW.action,
        NEW.direction, NEW.mode, NEW.migration_revision, NEW.content_hash, NEW.chain_root_id,
        NEW.reverts_execution_id, NEW.resumes_execution_id, NEW.enqueued_by, NEW.enqueued_by_name,
        NEW.enqueued_at)
        IS DISTINCT FROM
       (OLD.id, OLD.database_id, OLD.number, OLD.migration_id, OLD.change_request_id, OLD.action,
        OLD.direction, OLD.mode, OLD.migration_revision, OLD.content_hash, OLD.chain_root_id,
        OLD.reverts_execution_id, OLD.resumes_execution_id, OLD.enqueued_by, OLD.enqueued_by_name,
        OLD.enqueued_at) THEN
        RAISE EXCEPTION 'Migration execution input is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.status IN ('succeeded', 'failed', 'partial', 'partially_reverted', 'cancelled')
        AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'Migration execution is terminal' USING ERRCODE = '23514';
    END IF;
    IF OLD.status = 'outcome_unknown' AND NEW.status <> 'outcome_unknown'
        AND NOT (NEW.status = 'queued' AND NEW.reconcile_requested) THEN
        RAISE EXCEPTION 'Unknown outcome is only resolved by reconcile' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER migration_executions_protected BEFORE UPDATE ON migration_executions
FOR EACH ROW EXECUTE FUNCTION protect_migration_execution();

-- Mirror of the target checkpoints for the UI; resynchronised from the target when they disagree.
CREATE TABLE migration_batches (
    execution_id uuid NOT NULL REFERENCES migration_executions (id),
    batch_no integer NOT NULL CHECK (batch_no >= 1),
    direction text NOT NULL CHECK (direction IN ('up', 'down')),
    cursor_from bigint NOT NULL,
    cursor_to bigint NOT NULL,
    rows integer NOT NULL CHECK (rows >= 1),
    committed_at timestamptz NOT NULL,
    PRIMARY KEY (execution_id, batch_no),
    CHECK (cursor_to > cursor_from)
);

-- Mirror of the target history, written when an execution is finalised.
CREATE TABLE migration_history (
    database_id uuid NOT NULL,
    seq bigint NOT NULL CHECK (seq BETWEEN 1 AND 9007199254740991),
    execution_id uuid NOT NULL UNIQUE,
    migration_id uuid NOT NULL,
    migration_revision bigint NOT NULL,
    action text NOT NULL CHECK (action IN ('apply', 'rollback', 'forward')),
    version bigint NOT NULL CHECK (version BETWEEN 1 AND 9007199254740991),
    head_after bigint NOT NULL CHECK (head_after BETWEEN 0 AND 9007199254740991),
    reverts_execution_id uuid,
    sql_executed text NOT NULL,
    sql_hash bytea NOT NULL CHECK (octet_length(sql_hash) = 32),
    mode text NOT NULL CHECK (mode IN ('normal', 'batch')),
    batches integer NOT NULL DEFAULT 0 CHECK (batches >= 0),
    rows bigint NOT NULL DEFAULT 0 CHECK (rows >= 0),
    actor_id uuid NOT NULL REFERENCES users (id),
    actor_name text NOT NULL,
    committed_at timestamptz NOT NULL,
    PRIMARY KEY (database_id, seq),
    FOREIGN KEY (database_id, execution_id) REFERENCES migration_executions (database_id, id),
    FOREIGN KEY (database_id, migration_id) REFERENCES migrations (database_id, id),
    FOREIGN KEY (migration_id, migration_revision) REFERENCES migration_revisions (migration_id, revision),
    FOREIGN KEY (database_id, reverts_execution_id) REFERENCES migration_executions (database_id, id),
    CHECK ((action = 'rollback') = (reverts_execution_id IS NOT NULL))
);

CREATE TRIGGER migration_history_append_only BEFORE UPDATE OR DELETE ON migration_history
FOR EACH ROW EXECUTE FUNCTION reject_append_only_change();
