-- Shared SQLite target fixture. Practice data only.
-- Tests apply this to a temp file per case; foreign_keys must be ON on the connection.
CREATE TABLE items (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    price      NUMERIC,
    note       TEXT,                       -- nullable, contains NULLs
    payload    BLOB,
    active     INTEGER NOT NULL DEFAULT 1,
    created_at TEXT,
    CHECK (price IS NULL OR price >= 0)
);
INSERT INTO items (id, name, price, note, payload, created_at) VALUES
    (2,                   'Contoh kedua', 12.5, NULL,  x'deadbeef', '2026-10-20T08:00:00Z'),
    (9007199254740993,    'Contoh',       0,    'bigint melebihi 2^53', NULL, NULL),
    (9223372036854775807, 'Maksimum',     0,    '',    x'',        '2026-10-21T00:00:00Z');

CREATE TABLE orders (
    id      INTEGER PRIMARY KEY,
    item_id INTEGER REFERENCES items (id) ON UPDATE NO ACTION ON DELETE CASCADE
);
INSERT INTO orders VALUES (1, 2), (2, 9007199254740993);
CREATE INDEX orders_item_idx ON orders (item_id);

CREATE TABLE order_lines (
    order_id INTEGER NOT NULL REFERENCES orders (id),
    line_no  INTEGER NOT NULL,
    qty      INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (order_id, line_no)
);
CREATE TABLE shipments (
    order_id INTEGER NOT NULL,
    line_no  INTEGER NOT NULL,
    carrier  TEXT,
    FOREIGN KEY (order_id, line_no) REFERENCES order_lines (order_id, line_no)
);
CREATE INDEX shipments_carrier_order_idx ON shipments (carrier, order_id);
INSERT INTO order_lines VALUES (1, 1, 3), (1, 2, 1);
INSERT INTO shipments VALUES (1, 1, 'JNE');

-- no primary key. WITHOUT ROWID is not used, so rowid ordering is not a stable contract.
CREATE TABLE audit_log (message TEXT, logged_at TEXT);
INSERT INTO audit_log VALUES ('start', '2026-10-21T01:00:00Z'), ('stop', NULL);

CREATE TABLE empty_table (id INTEGER PRIMARY KEY, label TEXT);

CREATE TABLE "Odd ""Name""" ("select" INTEGER PRIMARY KEY, "Mixed Case" TEXT);
INSERT INTO "Odd ""Name""" VALUES (1, 'quoted');
