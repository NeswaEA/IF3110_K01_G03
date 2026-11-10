-- A leader must be a member of the group: removing the leader's membership is rejected by this key.
ALTER TABLE groups ADD COLUMN leader_user_id uuid;

ALTER TABLE groups ADD CONSTRAINT groups_leader_membership_fkey
    FOREIGN KEY (id, leader_user_id) REFERENCES group_memberships (group_id, user_id);
