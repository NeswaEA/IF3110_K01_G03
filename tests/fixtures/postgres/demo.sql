-- Shared PostgreSQL target fixture. Practice data only.
-- Loaded once by the target-postgres container after infra/postgres/init/01-roles.sh,
-- and by integration tests into an isolated schema/database.
-- Objects are owned by platform_editor so the runner can read them but not rewrite history.
SET ROLE platform_editor;

CREATE TABLE public.items (
    id         bigint PRIMARY KEY,
    name       text NOT NULL,
    price      numeric(20, 4),
    note       text,                       -- nullable, contains NULLs
    payload    bytea,                      -- binary
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz,
    CONSTRAINT items_name_key UNIQUE (name),
    CONSTRAINT items_price_check CHECK (price IS NULL OR price >= 0)
);

INSERT INTO public.items (id, name, price, note, payload, created_at) VALUES
    (2,                   'Contoh kedua', 12.5000,                  NULL,   '\xdeadbeef', '2026-10-20T08:00:00Z'),
    (9007199254740993,    'Contoh',       12345678901234.1234,      'bigint melebihi 2^53', NULL, NULL),
    (9223372036854775807, 'Maksimum',     0,                        '',     '\x',        '2026-10-21T00:00:00Z');

CREATE TABLE public.orders (
    id      bigint PRIMARY KEY,
    item_id bigint REFERENCES public.items (id) ON UPDATE NO ACTION ON DELETE CASCADE
);
INSERT INTO public.orders VALUES (1, 2), (2, 9007199254740993);
CREATE INDEX orders_item_idx ON public.orders (item_id);

-- composite PK, composite FK and composite index (column order must be preserved)
CREATE TABLE public.order_lines (
    order_id bigint  NOT NULL REFERENCES public.orders (id),
    line_no  integer NOT NULL,
    qty      integer NOT NULL DEFAULT 1,
    PRIMARY KEY (order_id, line_no)
);
CREATE TABLE public.shipments (
    order_id bigint  NOT NULL,
    line_no  integer NOT NULL,
    carrier  text,
    FOREIGN KEY (order_id, line_no) REFERENCES public.order_lines (order_id, line_no)
);
CREATE INDEX shipments_carrier_order_idx ON public.shipments (carrier, order_id);
INSERT INTO public.order_lines VALUES (1, 1, 3), (1, 2, 1);
INSERT INTO public.shipments VALUES (1, 1, 'JNE');

-- no primary key: order_stable must be false
CREATE TABLE public.audit_log (
    message   text,
    logged_at timestamptz
);
INSERT INTO public.audit_log VALUES ('start', '2026-10-21T01:00:00Z'), ('stop', NULL);

-- empty table: columns still listed, rows []
CREATE TABLE public.empty_table (id integer PRIMARY KEY, label text);

-- identifiers that need quoting
CREATE TABLE public."Odd ""Name""" (
    "select"     integer PRIMARY KEY,
    "Mixed Case" text
);
INSERT INTO public."Odd ""Name""" VALUES (1, 'quoted');

RESET ROLE;
