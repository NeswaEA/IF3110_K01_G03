-- Migration history of every attempt, committed or not. migration_history keeps
-- only commits because the head position is derived from it.
CREATE TABLE migration_attempts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    database_id uuid NOT NULL,
    execution_id uuid NOT NULL,
    migration_id uuid NOT NULL,
    migration_revision bigint NOT NULL,
    action text NOT NULL,
    attempt integer NOT NULL CHECK (attempt >= 0),
    status text NOT NULL CHECK (status IN ('succeeded', 'failed', 'partial', 'partially_reverted', 'outcome_unknown')),
    retry_scheduled boolean NOT NULL,
    sql_executed text,
    error_code text,
    error_message text,
    reverts_execution_id uuid,
    history_seq bigint,
    actor_id uuid NOT NULL REFERENCES users (id),
    actor_name text NOT NULL,
    recorded_at timestamptz NOT NULL,
    FOREIGN KEY (database_id, execution_id) REFERENCES migration_executions (database_id, id),
    FOREIGN KEY (migration_id, migration_revision) REFERENCES migration_revisions (migration_id, revision),
    CHECK ((status = 'succeeded') = (history_seq IS NOT NULL)),
    CHECK (status <> 'succeeded' OR sql_executed IS NOT NULL)
);

CREATE INDEX migration_attempts_execution_idx ON migration_attempts (execution_id, id);

CREATE TRIGGER migration_attempts_append_only BEFORE UPDATE OR DELETE ON migration_attempts
FOR EACH ROW EXECUTE FUNCTION reject_append_only_change();
