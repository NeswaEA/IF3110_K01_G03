-- Written in the enqueue transaction; the relay publishes unpublished rows to JetStream.
CREATE TABLE outbox_messages (
    id uuid PRIMARY KEY,
    subject text NOT NULL CHECK (subject <> ''),
    payload jsonb NOT NULL,
    headers jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(headers) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    publish_attempts integer NOT NULL DEFAULT 0 CHECK (publish_attempts >= 0),
    last_error text
);

CREATE INDEX outbox_messages_unpublished_idx ON outbox_messages (id) WHERE published_at IS NULL;
