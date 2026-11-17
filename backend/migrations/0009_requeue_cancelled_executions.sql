ALTER TABLE migration_executions DROP CONSTRAINT migration_executions_change_request_id_key;

-- Keep cancelled attempts in history while allowing their request to be queued again.
CREATE UNIQUE INDEX migration_executions_request_idx ON migration_executions (change_request_id)
    WHERE change_request_id IS NOT NULL AND status <> 'cancelled';
