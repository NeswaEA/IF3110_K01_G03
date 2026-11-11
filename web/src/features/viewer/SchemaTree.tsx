import { useQuery } from "@tanstack/react-query";
import { KeyRound, Table2 } from "lucide-react";
import type { Schemas } from "@/api/client";
import { schemaQuery } from "@/api/queries/databases";
import { DataTable } from "@/components/DataTable";
import { EmptyState } from "@/components/EmptyState";
import { ErrorState } from "@/components/ErrorState";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";

type Table = Schemas["Table"];

export function SchemaTree({ databaseId, onOpenTable }: { databaseId: string; onOpenTable: (schema: string, table: string) => void }) {
  const query = useQuery(schemaQuery(databaseId));
  if (query.isPending) return <Skeleton className="h-40 w-full" aria-label="Memuat schema" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const schemas = query.data.schemas.filter((s) => s.tables.length > 0);
  if (schemas.length === 0) {
    return <EmptyState icon={Table2} title="Belum ada tabel" description="Database ini belum memiliki tabel yang dapat dilihat." />;
  }
  return (
    <div className="grid gap-6">
      {schemas.map((s) => (
        <section key={s.name} aria-labelledby={`schema-${s.name}`}>
          <h2 id={`schema-${s.name}`} className="mb-2 text-lg font-semibold">
            Schema <code>{s.name}</code>
          </h2>
          <ul className="grid gap-2">
            {s.tables.map((t) => (
              <li key={t.name}>
                <details className="rounded-lg border">
                  <summary className="flex min-h-10 cursor-pointer items-center gap-2 px-3 py-2 font-medium">
                    <Table2 aria-hidden className="size-4 shrink-0" />
                    <span className="truncate">{t.name}</span>
                    <span className="ml-auto text-sm font-normal text-muted-foreground">{t.columns.length} kolom</span>
                  </summary>
                  <TableDetail table={t} onOpen={() => onOpenTable(s.name, t.name)} />
                </details>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}

function TableDetail({ table, onOpen }: { table: Table; onOpen: () => void }) {
  const pk = new Set(table.constraints.filter((c) => c.kind === "primary_key").flatMap((c) => c.columns));
  return (
    <div className="grid gap-4 border-t p-3">
      <DataTable
        caption={`Kolom tabel ${table.name}`}
        rows={table.columns}
        rowKey={(c) => c.name}
        maxHeight="none"
        columns={[
          {
            id: "name",
            header: "Kolom",
            cell: (c) => (
              <span className="inline-flex items-center gap-1 font-mono">
                {pk.has(c.name) && <KeyRound aria-label="Primary key" className="size-3.5" />}
                {c.name}
              </span>
            ),
          },
          { id: "type", header: "Tipe", cell: (c) => <code>{c.data_type}</code> },
          { id: "null", header: "Null", cell: (c) => (c.nullable ? "ya" : "tidak") },
          { id: "default", header: "Default", cell: (c) => (c.default === null ? <em className="text-muted-foreground">—</em> : <code>{c.default}</code>) },
        ]}
      />
      {table.constraints.length > 0 && (
        <div>
          <h3 className="mb-1 text-sm font-semibold">Constraint</h3>
          <ul className="grid gap-1 text-sm">
            {table.constraints.map((c, i) => (
              <li key={`${c.name ?? "anon"}-${i}`} className="flex flex-wrap items-baseline gap-2">
                <Badge variant="outline">{c.kind}</Badge>
                <code className="break-all">{c.definition}</code>
              </li>
            ))}
          </ul>
        </div>
      )}
      {table.indexes.length > 0 && (
        <div>
          <h3 className="mb-1 text-sm font-semibold">Indeks</h3>
          <ul className="grid gap-1 text-sm">
            {table.indexes.map((ix) => (
              <li key={ix.name}>
                <code className="break-all">{ix.definition || ix.name}</code>
              </li>
            ))}
          </ul>
        </div>
      )}
      <Button type="button" variant="outline" className="h-10 justify-self-start" onClick={onOpen}>
        Lihat data
      </Button>
    </div>
  );
}
