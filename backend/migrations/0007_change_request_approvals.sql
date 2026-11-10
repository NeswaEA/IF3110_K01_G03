CREATE TABLE change_request_approvals (
    id uuid PRIMARY KEY,
    request_id uuid NOT NULL REFERENCES change_requests (id),
    -- a deleted group is no longer required, so its decisions go with it
    group_id uuid NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    approver_id uuid NOT NULL REFERENCES users (id),
    decision text NOT NULL CHECK (decision IN ('approved', 'rejected')),
    comment text CHECK (char_length(comment) BETWEEN 1 AND 2000),
    migration_revision bigint NOT NULL CHECK (migration_revision BETWEEN 1 AND 9007199254740991),
    created_at timestamptz NOT NULL,
    dismissed_at timestamptz,
    CHECK (decision = 'approved' OR comment IS NOT NULL),
    CHECK (dismissed_at IS NULL OR dismissed_at >= created_at)
);

CREATE UNIQUE INDEX change_request_approvals_active_idx ON change_request_approvals (request_id, group_id)
    WHERE dismissed_at IS NULL;

CREATE FUNCTION protect_change_request_approval() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.dismissed_at IS NOT NULL OR NEW.dismissed_at IS NULL
        OR (NEW.id, NEW.request_id, NEW.group_id, NEW.approver_id, NEW.decision, NEW.comment,
            NEW.migration_revision, NEW.created_at)
        IS DISTINCT FROM
           (OLD.id, OLD.request_id, OLD.group_id, OLD.approver_id, OLD.decision, OLD.comment,
            OLD.migration_revision, OLD.created_at) THEN
        RAISE EXCEPTION 'An approval can only be dismissed once' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER change_request_approvals_dismiss_only BEFORE UPDATE ON change_request_approvals
FOR EACH ROW EXECUTE FUNCTION protect_change_request_approval();
