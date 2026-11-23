-- Keep evidence across worker replacement even before an attempt can finalize.
ALTER TABLE migration_executions
    ADD COLUMN attempt_progress_sql text,
    ADD COLUMN commit_attempt integer CHECK (commit_attempt >= 0);

-- A batch can commit its history without sending a user statement, for example
-- when all its checkpoints were already committed by an earlier attempt.
ALTER TABLE migration_attempts DROP CONSTRAINT migration_attempts_check1;
