import { useId, useState, type FormEvent } from "react";
import { type Schemas } from "@/api/client";
import type { CreateMigrationBody } from "@/api/queries/migrations";
import { ErrorState } from "@/components/ErrorState";
import { SqlEditor } from "@/components/SqlEditor";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { ExplainPanel } from "@/features/explain/ExplainPanel";
import { UnsavedDialog } from "./shared";

type Draft = {
  title: string;
  description: string;
  up_sql: string;
  down_sql: string;
  irreversible_reason: string;
  irreversible: boolean;
  mode: Schemas["MigrationMode"];
  batch: Schemas["BatchConfig"];
};

const initialBatch: Schemas["BatchConfig"] = { schema: "public", table: "", key_column: "id", batch_size: 1000, pause_ms: 0 };

export function MigrationForm({ database, migration, up = "", down = "", pending, error, onSave }: {
  database: Pick<Schemas["DatabaseSummary"], "id" | "effective_access" | "driver" | "capabilities">;
  migration?: Schemas["Migration"];
  up?: string;
  down?: string;
  pending: boolean;
  error: unknown;
  onSave: (draft: CreateMigrationBody, revision?: number) => void;
}) {
  const [selection, setSelection] = useState(migration?.current.up_sql ?? up);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [revision, setRevision] = useState<number | undefined>();
  const locked = migration?.locked ?? false;
  const readOnly = database.effective_access !== "edit";
  const prefix = useId();
  const source: Draft = {
    title: migration?.title ?? "",
    description: migration?.description ?? "",
    up_sql: migration?.current.up_sql ?? up,
    down_sql: migration?.current.down_sql ?? down,
    irreversible_reason: migration?.current.irreversible_reason ?? "",
    irreversible: migration?.current.irreversible_reason != null,
    mode: migration?.current.mode ?? "normal",
    batch: migration?.current.batch ?? initialBatch,
  };
  const value = draft ?? source;
  const dirty = draft !== null && JSON.stringify(draft) !== JSON.stringify(source);
  function set<K extends keyof Draft>(key: K, next: Draft[K]) {
    if (draft === null) setRevision(migration?.current.revision);
    setDraft({ ...value, [key]: next });
  }
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const body = {
      title: value.title, description: value.description, up_sql: value.up_sql,
      mode: value.mode, batch: value.mode === "batch" ? value.batch : null,
    };
    onSave(value.irreversible ? {...body, irreversible_reason: value.irreversible_reason}
      : {...body, down_sql: value.down_sql}, revision ?? migration?.current.revision);
  }

  return <form onSubmit={submit} className="grid min-w-0 gap-4">
    <UnsavedDialog dirty={dirty} />
    {error != null && <ErrorState error={error} />}
    <div className="grid gap-2">
      <label htmlFor={prefix+"title"}>Judul migrasi</label>
      <Input id={prefix+"title"} value={value.title} onChange={(e) => set("title", e.target.value)} readOnly={readOnly} maxLength={200} required />
      <label htmlFor={prefix+"description"}>Deskripsi</label>
      <Textarea id={prefix+"description"} value={value.description} onChange={(e) => set("description", e.target.value)} readOnly={readOnly} maxLength={2000}/>
    </div>
    <label className="grid gap-1 text-sm">Mode
      <select className="rounded-md border bg-background p-2" value={value.mode} disabled={readOnly || locked}
        onChange={(e) => set("mode", e.target.value === "batch" ? "batch" : "normal")}>
        <option value="normal">Normal</option>
        {database.capabilities.batch_migrations && <option value="batch">Batch</option>}
      </select>
    </label>
    {value.mode === "batch" && <fieldset disabled={readOnly || locked} className="grid gap-3 rounded-md border p-3 sm:grid-cols-2">
      <legend className="px-1 text-sm font-medium">Konfigurasi batch</legend>
      {(["schema", "table", "key_column", "batch_size", "pause_ms"] as const).map((key) => {
        const numeric = key === "batch_size" || key === "pause_ms";
        const labels = {schema:"Schema",table:"Tabel",key_column:"Kolom key",batch_size:"Ukuran batch",pause_ms:"Jeda antarbatch (ms)"};
        return <label className="grid gap-1 text-sm" key={key}>{labels[key]}
          <Input type={numeric ? "number" : "text"} value={value.batch[key]} required
            min={key === "batch_size" ? 1 : 0} max={key === "pause_ms" ? 60000 : undefined}
            onChange={(e) => set("batch", {...value.batch, [key]: numeric ? Number(e.target.value) : e.target.value})} />
        </label>;
      })}
    </fieldset>}
    <SqlEditor label="SQL up" driver={database.driver} value={value.up_sql} readOnly={readOnly || locked}
      onChange={(sql) => set("up_sql", sql)} onSelection={setSelection} />
    <label className="flex items-center gap-2 text-sm">
      <input type="checkbox" checked={value.irreversible} disabled={readOnly || locked} onChange={(e) => set("irreversible", e.target.checked)} />
      Irreversible
    </label>
    {value.irreversible ? <label className="grid gap-1">Alasan irreversible
      <Input value={value.irreversible_reason} readOnly={readOnly || locked} maxLength={500} required
        onChange={(e) => set("irreversible_reason", e.target.value)} /></label>
      : <SqlEditor label="SQL down" driver={database.driver} value={value.down_sql} readOnly={readOnly || locked}
        onChange={(sql) => set("down_sql", sql)} onSelection={setSelection} />}
    {database.capabilities.explain && <ExplainPanel databaseId={database.id} sql={selection} canExplain={!readOnly} />}
    {!readOnly && <Button type="submit" className="justify-self-start" disabled={pending || (!!migration && !dirty)}>Simpan migrasi</Button>}
  </form>;
}
