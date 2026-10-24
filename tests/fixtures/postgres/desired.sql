SET ROLE platform_editor;

ALTER TABLE public.items ADD COLUMN status text NOT NULL DEFAULT 'active';
CREATE INDEX items_status_idx ON public.items(status);

CREATE TABLE public.item_tags (
    id bigint PRIMARY KEY,
    item_id bigint NOT NULL REFERENCES public.items(id),
    label text NOT NULL,
    UNIQUE (item_id, label),
    CHECK (length(label) > 0)
);

RESET ROLE;
