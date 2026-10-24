ALTER TABLE items ADD COLUMN status TEXT NOT NULL DEFAULT 'active';
CREATE INDEX items_status_idx ON items(status);

CREATE TABLE item_tags (
    id INTEGER PRIMARY KEY,
    item_id INTEGER NOT NULL REFERENCES items(id),
    label TEXT NOT NULL,
    UNIQUE (item_id, label),
    CHECK (length(label) > 0)
);
